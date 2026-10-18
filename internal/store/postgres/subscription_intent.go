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
	"reflect"
)

type SubscriptionIntentStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewSubscriptionIntentStore(pool *pgxpool.Pool, ids ports.IDGen) *SubscriptionIntentStore {
	return &SubscriptionIntentStore{pool, ids}
}

var _ ports.SubscriptionIntentUoW = (*SubscriptionIntentStore)(nil)
var _ ports.SubscriptionMediaRepository = (*SubscriptionIntentStore)(nil)
var _ ports.SubscriptionDetailReader = (*SubscriptionIntentStore)(nil)

type subscriptionIntentTx struct {
	tx      pgx.Tx
	q       *db.Queries
	ids     ports.IDGen
	actor   *ports.Actor
	create  *ports.SubscriptionCreateRequest
	frozen  *ports.SubscriptionCreateContext
	owned   *db.Subscription
	wanted  []db.WantedItem
	applied bool
}

func (s *SubscriptionIntentStore) Within(ctx context.Context, fn func(ports.SubscriptionIntentTx) error) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return subscriptionIntentError(ctx, e)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	t := &subscriptionIntentTx{tx: tx, q: db.New(tx), ids: s.ids}
	if e = fn(t); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return subscriptionIntentError(ctx, e)
	}
	return nil
}
func subscriptionIntentError(ctx context.Context, e error) error {
	if e == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	for _, known := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrInvalidSubscriptionBindings, ports.ErrSubscriptionBindingsConflict} {
		if errors.Is(e, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}

func (t *subscriptionIntentTx) LockCreate(ctx context.Context, req ports.SubscriptionCreateRequest) (out ports.SubscriptionCreateContext, err error) {
	defer func() { err = subscriptionIntentError(ctx, err) }()
	if t.actor != nil || t.applied {
		return out, ports.ErrSubscriptionBindingsConflict
	}
	if err = lockAcquisitionUser(ctx, t.q, &req.Actor); err != nil {
		return out, err
	}
	if !req.Actor.CanAccessUser(req.OwnerID.String()) {
		return out, ports.ErrForbidden
	}
	policy, e := t.q.LockSearchPolicy(ctx, req.Command.QualityPolicyID)
	if e != nil {
		return out, e
	}
	mediaID := req.Command.MediaID
	if req.Command.MediaInput != nil {
		mediaID, e = t.ensureMedia(ctx, *req.Command.MediaInput, req.MediaUnits)
		if e != nil {
			return out, e
		}
	}
	if err = t.q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription.intent", AggregateID: req.OwnerID.String() + ":" + mediaID.String() + ":" + req.Command.RootID.String()}); err != nil {
		return out, err
	}
	existing, e := t.q.GetSubscriptionByUserMediaRoot(ctx, db.GetSubscriptionByUserMediaRootParams{UserID: req.OwnerID, MediaID: mediaID, RootID: req.Command.RootID})
	if e == nil {
		existing, e = t.q.LockSearchSubscription(ctx, existing.ID)
		if e != nil {
			return out, e
		}
		record, e := subscriptionIntentRecord(existing)
		if e != nil {
			return out, e
		}
		out.Existing = &record
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	root, e := t.q.LockAcquisitionRoot(ctx, req.Command.RootID)
	if e != nil {
		return out, e
	}
	units, e := t.q.LockScopeMediaUnits(ctx, mediaID)
	if e != nil {
		return out, e
	}
	media, e := t.q.LockSubmissionMedia(ctx, mediaID)
	if e != nil {
		return out, e
	}
	if req.Command.MediaInput != nil && media.MediaType != req.Command.MediaInput.MediaType {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	out.MediaID = mediaID
	out.MediaType = media.MediaType
	out.RootMediaType = root.MediaType
	out.RootHealth = root.Health
	out.PolicyRevision = policy.CurrentRevision
	for _, u := range units {
		out.Units = append(out.Units, ports.SubscriptionIntentUnit{ID: u.ID, UnitKey: u.UnitKey, SeasonNumber: intentInt4(u.SeasonNumber), EpisodeNumber: intentInt4(u.EpisodeNumber), Retired: u.Retired})
	}
	t.actor = &req.Actor
	t.create = &req
	// Freeze a separate copy so callers cannot mutate store authorization material.
	raw, _ := json.Marshal(out)
	var frozen ports.SubscriptionCreateContext
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return out, err
	}
	t.frozen = &frozen
	return out, nil
}

func (t *subscriptionIntentTx) ApplyCreate(ctx context.Context, d ports.SubscriptionCreateDecision) (out *ports.SubscriptionRecord, wanted []*ports.SubscriptionWanted, err error) {
	defer func() { err = subscriptionIntentError(ctx, err) }()
	if t.create == nil || t.applied {
		return nil, nil, ports.ErrSubscriptionBindingsConflict
	}
	c, f, r := d.Command, t.frozen, t.create
	if c.MediaID != f.MediaID || c.RootID != r.Command.RootID || c.QualityPolicyID != r.Command.QualityPolicyID || c.QualityPolicyRevision != f.PolicyRevision || len(d.Wanted) == 0 {
		return nil, nil, ports.ErrSubscriptionBindingsConflict
	}
	// All units must still be the frozen set; row locks cover their versions.
	allowed := map[uuid.UUID]bool{}
	for _, u := range f.Units {
		allowed[u.ID] = true
	}
	seen := map[uuid.UUID]bool{}
	for _, w := range d.Wanted {
		if !allowed[w.UnitID] || seen[w.UnitID] {
			return nil, nil, ports.ErrSubscriptionBindingsConflict
		}
		seen[w.UnitID] = true
	}
	for _, id := range c.IndexerIDs {
		if _, e := t.q.GetIndexerByID(ctx, id); e != nil {
			return nil, nil, e
		}
	}
	if c.DownloaderID != nil {
		if _, e := t.q.GetDownloadClientByID(ctx, *c.DownloaderID); e != nil {
			return nil, nil, e
		}
	}
	// Revalidate frozen rows after the caller's decision, before publishing writes.
	if err = lockAcquisitionUser(ctx, t.q, t.actor); err != nil {
		return nil, nil, err
	}
	if f.Existing != nil {
		sub, e := t.q.GetSubscriptionByID(ctx, f.Existing.ID)
		if e != nil {
			return nil, nil, e
		}
		current, e := subscriptionIntentRecord(sub)
		if e != nil {
			return nil, nil, e
		}
		if !reflect.DeepEqual(current, *f.Existing) {
			return nil, nil, ports.ErrSubscriptionBindingsConflict
		}
	}
	indexers, e := json.Marshal(c.IndexerIDs)
	if e != nil {
		return nil, nil, e
	}
	seasons, e := json.Marshal(c.Seasons)
	if e != nil {
		return nil, nil, e
	}
	selected, e := json.Marshal(c.SelectedUnitIDs)
	if e != nil {
		return nil, nil, e
	}
	var dl pgtype.UUID
	if c.DownloaderID != nil {
		dl = pgtype.UUID{Bytes: *c.DownloaderID, Valid: true}
	}
	var sub db.Subscription
	event := "subscription.created"
	if f.Existing == nil {
		sub, err = t.q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: t.ids.NewUUID(), UserID: r.OwnerID, MediaID: c.MediaID, RootID: c.RootID, QualityPolicyID: c.QualityPolicyID, QualityPolicyRevision: c.QualityPolicyRevision, IndexerIds: indexers, DownloaderID: dl, ScopeType: c.ScopeType})
		if err == nil {
			sub, err = t.q.InitializeSubscriptionTVScope(ctx, db.InitializeSubscriptionTVScopeParams{ID: sub.ID, Seasons: seasons, IncludeSpecials: c.IncludeSpecials})
		}
	} else {
		event = "subscription.updated"
		sub, err = t.q.ReconfigureSubscription(ctx, db.ReconfigureSubscriptionParams{ID: f.Existing.ID, QualityPolicyID: c.QualityPolicyID, QualityPolicyRevision: c.QualityPolicyRevision, IndexerIds: indexers, DownloaderID: dl, ScopeType: c.ScopeType, Seasons: seasons, IncludeSpecials: c.IncludeSpecials})
	}
	if err != nil {
		return nil, nil, err
	}
	if err = t.q.StoreSubscriptionSelectedUnits(ctx, db.StoreSubscriptionSelectedUnitsParams{ID: sub.ID, SelectedUnitIds: selected}); err != nil {
		return nil, nil, err
	}
	sub.SelectedUnitIds = selected
	if err = t.q.SkipWantedBeforeReconfiguration(ctx, db.SkipWantedBeforeReconfigurationParams{SubscriptionID: sub.ID, IntentVersion: sub.IntentVersion}); err != nil {
		return nil, nil, err
	}
	for _, w := range d.Wanted {
		row, e := t.q.ApplyScopeWanted(ctx, db.ApplyScopeWantedParams{ID: t.ids.NewUUID(), SubscriptionID: sub.ID, UnitID: w.UnitID, IntentVersion: sub.IntentVersion, IsSkipped: w.ScopeExcluded})
		if e != nil {
			return nil, nil, e
		}
		item := subscriptionIntentWanted(row)
		wanted = append(wanted, &item)
	}
	payload := map[string]any{"subscription_id": sub.ID.String(), "user_id": r.OwnerID.String(), "media_id": c.MediaID.String(), "units_count": len(wanted)}
	if err = t.publish(ctx, sub.ID, event, payload); err != nil {
		return nil, nil, err
	}
	audit, _ := json.Marshal(map[string]any{"intent_version": sub.IntentVersion, "scope_type": sub.ScopeType, "indexer_ids": c.IndexerIDs, "downloader_id": c.DownloaderID})
	if err = t.audit(ctx, sub.ID, event, audit); err != nil {
		return nil, nil, err
	}
	record, e := subscriptionIntentRecord(sub)
	if e != nil {
		return nil, nil, e
	}
	t.applied = true
	return &record, wanted, nil
}

func (t *subscriptionIntentTx) LockOwned(ctx context.Context, a *ports.Actor, id uuid.UUID) (out ports.SubscriptionOwnedContext, err error) {
	defer func() { err = subscriptionIntentError(ctx, err) }()
	if t.actor != nil || t.applied {
		return out, ports.ErrSubscriptionBindingsConflict
	}
	if err = lockAcquisitionUser(ctx, t.q, a); err != nil {
		return out, err
	}
	hint, e := t.q.GetSubscriptionByID(ctx, id)
	if e != nil {
		return out, e
	}
	if !a.CanAccessUser(hint.UserID.String()) {
		return out, ports.ErrForbidden
	}
	if _, err = t.q.LockSearchPolicy(ctx, hint.QualityPolicyID); err != nil {
		return out, err
	}
	sub, e := t.q.LockSearchSubscription(ctx, id)
	if e != nil {
		return out, e
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return out, ports.ErrForbidden
	}
	if sub.QualityPolicyID != hint.QualityPolicyID {
		return out, ports.ErrSubscriptionBindingsConflict
	}
	if _, err = t.q.LockAcquisitionRoot(ctx, sub.RootID); err != nil {
		return out, err
	}
	if _, err = t.q.LockScopeMediaUnits(ctx, sub.MediaID); err != nil {
		return out, err
	}
	if _, err = t.q.LockSubmissionMedia(ctx, sub.MediaID); err != nil {
		return out, err
	}
	rows, e := t.q.ListWantedItemsForSubscription(ctx, id)
	if e != nil {
		return out, e
	}
	out.Subscription, e = subscriptionIntentRecord(sub)
	if e != nil {
		return out, e
	}
	for _, row := range rows {
		out.Wanted = append(out.Wanted, subscriptionIntentWanted(row))
	}
	actor := *a
	t.actor = &actor
	t.owned = &sub
	t.wanted = rows
	return out, nil
}
func (t *subscriptionIntentTx) ApplyState(ctx context.Context, state string) (out *ports.SubscriptionRecord, err error) {
	defer func() { err = subscriptionIntentError(ctx, err) }()
	if t.owned == nil || t.applied {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	if err = lockAcquisitionUser(ctx, t.q, t.actor); err != nil {
		return nil, err
	}
	sub, e := t.q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: t.owned.ID, State: state})
	if e != nil {
		return nil, e
	}
	if err = t.finishOwned(ctx, sub, "state", map[string]any{"state": state}); err != nil {
		return nil, err
	}
	record, e := subscriptionIntentRecord(sub)
	if e != nil {
		return nil, e
	}
	t.applied = true
	return &record, nil
}
func (t *subscriptionIntentTx) ApplySkipped(ctx context.Context, unitID uuid.UUID, skipped bool) (err error) {
	defer func() { err = subscriptionIntentError(ctx, err) }()
	if t.owned == nil || t.applied {
		return ports.ErrSubscriptionBindingsConflict
	}
	found := false
	for _, w := range t.wanted {
		if w.UnitID == unitID {
			found = true
		}
	}
	if !found {
		return ports.ErrNotFound
	}
	if err = lockAcquisitionUser(ctx, t.q, t.actor); err != nil {
		return err
	}
	if _, err = t.q.SetUserWantedSkipped(ctx, db.SetUserWantedSkippedParams{SubscriptionID: t.owned.ID, UnitID: unitID, UserSkipped: skipped}); err != nil {
		return err
	}
	sub, e := t.q.UpdateSubscriptionState(ctx, db.UpdateSubscriptionStateParams{ID: t.owned.ID, State: t.owned.State})
	if e != nil {
		return e
	}
	if err = t.finishOwned(ctx, sub, "skip", map[string]any{"unit_id": unitID, "skipped": skipped}); err != nil {
		return err
	}
	t.applied = true
	return nil
}
func (t *subscriptionIntentTx) finishOwned(ctx context.Context, sub db.Subscription, action string, payload map[string]any) error {
	if e := t.q.RefreshBindingWantedIntents(ctx, db.RefreshBindingWantedIntentsParams{SubscriptionID: sub.ID, IntentVersion: sub.IntentVersion}); e != nil {
		return e
	}
	payload["id"] = sub.ID
	payload["state"] = sub.State
	payload["intent_version"] = sub.IntentVersion
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	if e = t.audit(ctx, sub.ID, "subscription.control_"+action, raw); e != nil {
		return e
	}
	return t.publish(ctx, sub.ID, "subscription.control_changed", payload)
}
func (t *subscriptionIntentTx) audit(ctx context.Context, id uuid.UUID, action string, payload []byte) error {
	_, e := t.q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: t.ids.NewUUID(), ActorType: string(t.actor.Type), ActorID: t.actor.ID, Action: action, TargetType: "subscription", TargetID: id.String(), Payload: payload})
	return e
}

func (s *SubscriptionIntentStore) ReadSubscriptionDetail(ctx context.Context, a *ports.Actor, id uuid.UUID) (sub *ports.SubscriptionRecord, wanted []*ports.SubscriptionWanted, err error) {
	err = s.Within(ctx, func(tx ports.SubscriptionIntentTx) error {
		current, e := tx.LockOwned(ctx, a, id)
		if e != nil {
			return e
		}
		sub = &current.Subscription
		for _, w := range current.Wanted {
			item := w
			wanted = append(wanted, &item)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return sub, wanted, nil
}
