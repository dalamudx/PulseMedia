package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
)

type SubscriptionControlStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewSubscriptionControlStore(pool *pgxpool.Pool, ids ports.IDGen) *SubscriptionControlStore {
	return &SubscriptionControlStore{pool, ids}
}
func (s *SubscriptionControlStore) ControlSubscription(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.SubscriptionControlCommand, key, hash string) (out *ports.SubscriptionControlReceipt, err error) {
	if e := acquisitionCommandActor(a); e != nil {
		return nil, e
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrInvalidSubscriptionBindings) && !errors.Is(err, ports.ErrSubscriptionBindingsConflict) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	hint, e := q.GetSubscriptionByID(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if !a.CanAccessUser(hint.UserID.String()) {
		return nil, ports.ErrForbidden
	}
	if _, e = q.LockSearchPolicy(ctx, hint.QualityPolicyID); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription.control.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, id)
	if e != nil {
		return nil, e
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return nil, ports.ErrForbidden
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		var result ports.SubscriptionControlReceipt
		if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &result) != nil || result.ID != id {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		return &result, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if sub.IntentVersion != c.ExpectedVersion || sub.QualityPolicyID != hint.QualityPolicyID {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	if _, e = q.LockAcquisitionRoot(ctx, sub.RootID); e != nil {
		return nil, e
	}
	items, e := q.ListWantedItemsForSubscription(ctx, id)
	if e != nil {
		return nil, e
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UnitID.String() < items[j].UnitID.String() })
	found := false
	for _, w := range items {
		if c.Action == "recheck" && (c.UnitID == nil || w.UnitID != *c.UnitID) {
			continue
		}
		if _, e = q.LockAcquisitionUnit(ctx, w.UnitID); e != nil {
			return nil, e
		}
		if c.UnitID != nil && w.UnitID == *c.UnitID {
			found = true
		}
	}
	if (c.Action == "skip" || c.Action == "recheck") && !found {
		return nil, ports.ErrNotFound
	}
	switch c.Action {
	case "state":
		if sub.State != c.State {
			sub, e = q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: id, State: c.State})
			if e != nil {
				return nil, e
			}
		}
	case "release_block":
		visible, err := q.HasSubscriptionCandidateEvidence(ctx, db.HasSubscriptionCandidateEvidenceParams{SubscriptionID: id, CandidateID: *c.CandidateID})
		if err != nil {
			return nil, err
		}
		if !visible.Valid || !visible.Bool {
			return nil, ports.ErrNotFound
		}
		if *c.Blocked {
			count, err := q.CountSubscriptionReleaseBlocks(ctx, id)
			if err != nil {
				return nil, err
			}
			already, err := q.IsSubscriptionReleaseBlocked(ctx, db.IsSubscriptionReleaseBlockedParams{SubscriptionID: id, CandidateID: *c.CandidateID})
			if err != nil {
				return nil, err
			}
			if count >= 100 && !already {
				return nil, ports.ErrInvalidSubscriptionBindings
			}
		}
		if e = q.SetSubscriptionReleaseBlock(ctx, db.SetSubscriptionReleaseBlockParams{SubscriptionID: id, CandidateID: *c.CandidateID, Blocked: *c.Blocked}); e != nil {
			return nil, e
		}
		sub, e = q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: id, State: sub.State})
		if e != nil {
			return nil, e
		}
	case "scope":
		sub, e = s.applyScope(ctx, q, sub, c)
		if e != nil {
			return nil, e
		}
	case "recheck":
		if sub.State != "active" {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		changed, err := q.RecheckSubscriptionEpisode(ctx, db.RecheckSubscriptionEpisodeParams{SubscriptionID: id, UnitID: *c.UnitID, IntentVersion: sub.IntentVersion})
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
	case "skip":
		if c.Skipped == nil {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
		if _, e = q.SetUserWantedSkipped(ctx, db.SetUserWantedSkippedParams{SubscriptionID: id, UnitID: *c.UnitID, UserSkipped: *c.Skipped}); e != nil {
			return nil, e
		}
		sub, e = q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: id, State: sub.State})
		if e != nil {
			return nil, e
		}
	default:
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	if c.Action != "recheck" {
		if e = q.RefreshBindingWantedIntents(ctx, db.RefreshBindingWantedIntentsParams{SubscriptionID: id, IntentVersion: sub.IntentVersion}); e != nil {
			return nil, e
		}
	}
	out = &ports.SubscriptionControlReceipt{ID: id, State: sub.State, IntentVersion: sub.IntentVersion, UnitID: c.UnitID, Skipped: c.Skipped, CandidateID: c.CandidateID, Blocked: c.Blocked}
	safe, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "subscription.control_" + c.Action, TargetType: "subscription", TargetID: id.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription", AggregateID: id.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "subscription", AggregateID: id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "subscription", AggregateID: id.String(), EventType: "subscription.control_changed", Seq: int64(seq), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: safe, ResourceID: pgtype.Text{String: id.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
