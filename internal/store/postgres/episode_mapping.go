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
)

type EpisodeMappingStore struct {
	*EpisodeSourceStore
	ids ports.IDGen
}

func NewEpisodeMappingStore(source *EpisodeSourceStore, ids ports.IDGen) *EpisodeMappingStore {
	return &EpisodeMappingStore{source, ids}
}
func cachedEpisodeMapping(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID, key, hash string) (*ports.EpisodeMappingReceipt, error) {
	if _, e := readTVRefresh(ctx, q, a, id); e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var r ports.EpisodeMappingReceipt
	if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &r) != nil || r.SubscriptionID != id || r.MappingRevision < 1 {
		return nil, ports.ErrEpisodeSourceConflict
	}
	return &r, nil
}
func (s *EpisodeMappingStore) ReadEpisodeMappingReceipt(ctx context.Context, a *ports.Actor, id uuid.UUID, key, hash string) (*ports.EpisodeMappingReceipt, error) {
	tx, e := s.refresh.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	out, e := cachedEpisodeMapping(ctx, q, a, id, key, hash)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
func (s *EpisodeMappingStore) PublishEpisodeMapping(ctx context.Context, a *ports.Actor, p ports.EpisodeMappingPreview, key, hash string) (out *ports.EpisodeMappingReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if !p.CanPublish || len(p.InputStamp) != 64 || len(p.SourceHash) != 64 || key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrEpisodeSourceConflict) && !errors.Is(err, ports.ErrInvalidEpisodeMapping) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.refresh.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	actorKey, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "episode.mapping.command", AggregateID: string(actorKey)}); e != nil {
		return nil, e
	}
	hint, e := q.GetSubscriptionByID(ctx, p.SubscriptionID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if _, e = q.LockSearchPolicy(ctx, hint.QualityPolicyID); e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, hint.ID)
	if e != nil {
		return nil, e
	}
	if sub.QualityPolicyID != hint.QualityPolicyID {
		return nil, ports.ErrEpisodeSourceConflict
	}
	cached, e := cachedEpisodeMapping(ctx, q, a, sub.ID, key, hash)
	if e != nil || cached != nil {
		return cached, e
	}
	if sub.State == "archived" {
		return nil, ports.ErrEpisodeSourceConflict
	}
	if _, e = q.LockAcquisitionRoot(ctx, sub.RootID); e != nil {
		return nil, e
	}
	if _, e = q.LockScopeMediaUnits(ctx, sub.MediaID); e != nil {
		return nil, e
	}
	if _, e = q.LockSubmissionMedia(ctx, sub.MediaID); e != nil {
		return nil, e
	}
	current, e := readEpisodeSourceContext(ctx, q, a, sub.ID)
	if e != nil {
		return nil, e
	}
	if current.InputStamp != p.InputStamp || current.MediaID != p.MediaID || current.MappingRevision != p.MappingRevision {
		return nil, ports.ErrEpisodeSourceConflict
	}
	units := map[uuid.UUID]ports.EpisodeSourceUnit{}
	for _, u := range current.Units {
		units[u.ID] = u
	}
	sources := map[int]bool{}
	targets := map[uuid.UUID]bool{}
	if p.Mode != "standard" && len(p.Rows) == 0 {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	if len(p.Rows) > 5000 {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	for _, r := range p.Rows {
		u, ok := units[r.UnitID]
		if !ok || u.Retired || u.Version != r.UnitVersion || u.Key != r.UnitKey || r.SourceNumber < 1 || r.SourceNumber > 9999 || sources[r.SourceNumber] || targets[r.UnitID] {
			return nil, ports.ErrEpisodeSourceConflict
		}
		if (p.Mode == "group" || p.Mode == "absolute") && (r.EpisodeID < 1 || u.EpisodeID != r.EpisodeID) {
			return nil, ports.ErrEpisodeSourceConflict
		}
		sources[r.SourceNumber] = true
		targets[r.UnitID] = true
	}
	rawSource := []byte(`{}`)
	if p.Source != nil {
		rawSource, _ = json.Marshal(p.Source)
	}
	rows, _ := json.Marshal(p.Rows)
	record, e := q.InsertEpisodeMappingRevision(ctx, db.InsertEpisodeMappingRevisionParams{SubscriptionID: sub.ID, Revision: current.MappingRevision + 1, MediaID: current.MediaID, MediaVersion: current.MediaVersion, IntentVersion: sub.IntentVersion + 1, Mode: p.Mode, GroupID: p.GroupID, PartID: p.PartID, InputStamp: p.InputStamp, SourceHash: p.SourceHash, SourceSnapshot: rawSource, Mappings: rows})
	if e != nil {
		return nil, e
	}
	changed, e := q.AdvanceEpisodeMappingBinding(ctx, db.AdvanceEpisodeMappingBindingParams{ID: sub.ID, EpisodeMappingRevision: pgtype.Int8{Int64: record.Revision, Valid: true}, IntentVersion: sub.IntentVersion, EpisodeMappingRevision_2: pgtype.Int8{Int64: current.MappingRevision, Valid: true}})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrEpisodeSourceConflict
	}
	if e != nil {
		return nil, e
	}
	if e = q.InvalidateEpisodeMappingWanted(ctx, db.InvalidateEpisodeMappingWantedParams{SubscriptionID: sub.ID, IntentVersion: changed.IntentVersion}); e != nil {
		return nil, e
	}
	out = &ports.EpisodeMappingReceipt{SubscriptionID: sub.ID, MappingRevision: record.Revision, IntentVersion: changed.IntentVersion, Mode: record.Mode, SourceHash: record.SourceHash}
	if record.ValidUntil.Valid {
		out.ValidUntil = &record.ValidUntil.Time
	}
	safe, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "episode_mapping.published", TargetType: "subscription", TargetID: sub.ID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription", AggregateID: sub.ID.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "subscription", AggregateID: sub.ID.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "subscription", AggregateID: sub.ID.String(), EventType: "episode_mapping.published", Seq: int64(seq), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: safe, ResourceID: pgtype.Text{String: sub.ID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *EpisodeMappingStore) ReadEpisodeMapping(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.EpisodeMappingState, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := s.refresh.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	out, e := readEpisodeMappingState(ctx, q, a, id)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
func readEpisodeMappingState(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID) (*ports.EpisodeMappingState, error) {
	current, e := readEpisodeSourceContext(ctx, q, a, id)
	if e != nil {
		return nil, e
	}
	observed, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out := &ports.EpisodeMappingState{TMDBID: current.TMDBID, MediaID: current.MediaID, InputStamp: current.InputStamp, IntentVersion: current.IntentVersion, ObservedAt: observed.Time, SubscriptionID: id, Mode: "standard", Current: true, Rows: []ports.EpisodeGroupMappingRow{}}
	r, e := q.GetCurrentEpisodeMapping(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out.MappingRevision = r.Revision
	out.Mode = r.Mode
	out.SourceHash = r.SourceHash
	if json.Unmarshal(r.Mappings, &out.Rows) != nil {
		return nil, ports.ErrUnavailable
	}
	if r.ValidUntil.Valid {
		out.ValidUntil = &r.ValidUntil.Time
		now, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return nil, ports.ErrUnavailable
		}
		if !r.ValidUntil.Time.After(now.Time) {
			out.Current = false
			out.HoldReason = "source_observation_expired"
		}
	}
	if current.MediaID != r.MediaID || current.MediaVersion != r.MediaVersion {
		out.Current = false
		out.HoldReason = "canonical_metadata_changed"
	}
	units := map[uuid.UUID]ports.EpisodeSourceUnit{}
	for _, u := range current.Units {
		units[u.ID] = u
	}
	for _, row := range out.Rows {
		u, ok := units[row.UnitID]
		if !ok || u.Retired || u.Key != row.UnitKey || u.Version != row.UnitVersion || (r.Mode != "manual" && u.EpisodeID != row.EpisodeID) {
			out.Current = false
			out.HoldReason = "canonical_units_changed"
		}
	}
	return out, nil
}
