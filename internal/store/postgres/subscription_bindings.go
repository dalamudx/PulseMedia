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

type SubscriptionBindingStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

var _ ports.SubscriptionBindingRepository = (*SubscriptionBindingStore)(nil)

func NewSubscriptionBindingStore(pool *pgxpool.Pool, ids ports.IDGen) *SubscriptionBindingStore {
	return &SubscriptionBindingStore{pool, ids}
}
func (s *SubscriptionBindingStore) ConfigureBindings(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.SubscriptionBindingsCommand, key, hash string) (*ports.SubscriptionBindingsInfo, error) {
	if e := acquisitionCommandActor(a); e != nil {
		return nil, e
	}
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
	desiredID := hint.QualityPolicyID
	if c.QualityPolicyID != nil {
		desiredID = *c.QualityPolicyID
	}
	policyIDs := []string{hint.QualityPolicyID}
	if desiredID != hint.QualityPolicyID {
		policyIDs = append(policyIDs, desiredID)
	}
	sort.Strings(policyIDs)
	var desiredPolicy db.Policy
	for _, policyID := range policyIDs {
		p, err := q.LockSearchPolicy(ctx, policyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
		if err != nil {
			return nil, err
		}
		if policyID == desiredID {
			desiredPolicy = p
		}
	}

	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription.bindings.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return nil, ports.ErrForbidden
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		if old.RequestHash != hash {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		var info ports.SubscriptionBindingsInfo
		if json.Unmarshal(old.ResponseBody, &info) != nil || info.ID != id {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		return &info, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if sub.IntentVersion != c.ExpectedVersion || sub.QualityPolicyID != hint.QualityPolicyID {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	for _, idx := range c.IndexerIDs {
		if _, e = q.GetIndexerByID(ctx, idx); errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrInvalidSubscriptionBindings
		} else if e != nil {
			return nil, e
		}
	}
	var dl pgtype.UUID
	if c.DownloaderID != nil {
		if _, e = q.GetDownloadClientByID(ctx, *c.DownloaderID); errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrInvalidSubscriptionBindings
		} else if e != nil {
			return nil, e
		}
		dl = pgtype.UUID{Bytes: *c.DownloaderID, Valid: true}
	}
	policyRevision := sub.QualityPolicyRevision
	if c.QualityPolicyID != nil {
		policyRevision = desiredPolicy.CurrentRevision
	}
	raw, _ := json.Marshal(c.IndexerIDs)
	sub, e = q.ConfigureSubscriptionBindings(ctx, db.ConfigureSubscriptionBindingsParams{ID: id, IndexerIds: raw, DownloaderID: dl, PolicyID: desiredPolicy.ID, PolicyRevision: policyRevision})
	if e != nil {
		return nil, e
	}
	if e = q.RefreshBindingWantedIntents(ctx, db.RefreshBindingWantedIntentsParams{SubscriptionID: id, IntentVersion: sub.IntentVersion}); e != nil {
		return nil, e
	}
	info := ports.SubscriptionBindingsInfo{ID: id, State: sub.State, IntentVersion: sub.IntentVersion, IndexerIDs: c.IndexerIDs, DownloaderID: c.DownloaderID}
	safe, _ := json.Marshal(info)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "subscription.bindings_configured", TargetType: "subscription", TargetID: id.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription", AggregateID: id.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "subscription", AggregateID: id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "subscription", AggregateID: id.String(), EventType: "subscription.bindings_configured", Payload: safe, Seq: int64(seq)}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: safe, ResourceID: pgtype.Text{String: id.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &info, nil
}
