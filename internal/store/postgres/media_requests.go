package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MediaRequestStore struct {
	pool  *pgxpool.Pool
	ids   ports.IDGen
	clock ports.Clock
}

var _ ports.MediaRequestRepository = (*MediaRequestStore)(nil)

func NewMediaRequestStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *MediaRequestStore {
	return &MediaRequestStore{pool: pool, ids: ids, clock: clock}
}

func mediaRequestKey(key string) bool {
	_, err := creationKey(key)
	return len(key) <= 80 && err == nil
}

func mediaRequestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	for _, allowed := range []error{ports.ErrMediaRequestConflict, ports.ErrMediaRequestLimit, ports.ErrRejected, ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrNotFound, ports.ErrUnavailable, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, allowed) {
			return allowed
		}
	}
	var sqlerr *pgconn.PgError
	if errors.As(err, &sqlerr) && sqlerr.Code == "23505" {
		return ports.ErrMediaRequestConflict
	}
	return ports.ErrUnavailable
}

func mediaRequestDTO(row db.MediaRequest) (ports.MediaRequest, error) {
	r := ports.MediaRequest{ID: row.ID, OwnerID: row.OwnerID, Intent: mediarequest.Intent{MediaType: row.MediaType, TMDBID: row.TmdbID, Seasons: make([]int, len(row.Seasons)), IncludeSpecials: row.IncludeSpecials}, State: row.State, Version: row.Version, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), Reason: row.Reason}
	for i, n := range row.Seasons {
		r.Intent.Seasons[i] = int(n)
	}
	intent, err := mediarequest.CanonicalIntent(r.Intent)
	reason, reasonErr := mediarequest.CanonicalReason(r.Reason)
	if err != nil || !reflect.DeepEqual(intent, r.Intent) || reasonErr != nil || reason != r.Reason || r.ID == uuid.Nil || r.OwnerID == uuid.Nil || !mediaRequestKey(row.CreationKey) || !creationHash(row.InputHash) || !row.CreatedAt.Valid || !row.UpdatedAt.Valid || row.CreatedAt.InfinityModifier != pgtype.Finite || row.UpdatedAt.InfinityModifier != pgtype.Finite || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return ports.MediaRequest{}, ports.ErrUnavailable
	}
	if row.DecisionActorID.Valid {
		v := uuid.UUID(row.DecisionActorID.Bytes)
		r.DecidedBy = &v
	}
	if row.DecidedAt.Valid {
		v := row.DecidedAt.Time.UTC()
		r.DecidedAt = &v
	}
	if row.SubscriptionID.Valid {
		v := uuid.UUID(row.SubscriptionID.Bytes)
		r.SubscriptionID = &v
	}
	if r.State == mediarequest.Pending {
		if r.Version != 1 || !r.UpdatedAt.Equal(r.CreatedAt) || r.DecidedBy != nil || r.DecidedAt != nil || r.SubscriptionID != nil || r.Reason != "" || row.DecisionKey.Valid || row.DecisionHash.Valid {
			return ports.MediaRequest{}, ports.ErrUnavailable
		}
	} else {
		if r.Version != 2 || r.DecidedBy == nil || *r.DecidedBy == uuid.Nil || r.DecidedAt == nil || r.DecidedAt.IsZero() || r.DecidedAt.Before(r.CreatedAt) || row.DecidedAt.InfinityModifier != pgtype.Finite || !row.DecisionKey.Valid || !mediaRequestKey(row.DecisionKey.String) || !row.DecisionHash.Valid || !creationHash(row.DecisionHash.String) {
			return ports.MediaRequest{}, ports.ErrUnavailable
		}
		switch r.State {
		case mediarequest.Approved:
			if r.SubscriptionID == nil || *r.SubscriptionID == uuid.Nil || r.Reason != "" {
				return ports.MediaRequest{}, ports.ErrUnavailable
			}
		case mediarequest.Rejected, mediarequest.Withdrawn:
			if r.SubscriptionID != nil || r.State == mediarequest.Withdrawn && *r.DecidedBy != r.OwnerID {
				return ports.MediaRequest{}, ports.ErrUnavailable
			}
		default:
			return ports.MediaRequest{}, ports.ErrUnavailable
		}
	}
	return r, nil
}

// Timestamp spellings may differ in JSONB and PostgreSQL's session timezone.
func mediaRequestEqual(a, b ports.MediaRequest) bool {
	a.CreatedAt, b.CreatedAt = a.CreatedAt.UTC(), b.CreatedAt.UTC()
	a.UpdatedAt, b.UpdatedAt = a.UpdatedAt.UTC(), b.UpdatedAt.UTC()
	if a.DecidedAt != nil {
		v := a.DecidedAt.UTC()
		a.DecidedAt = &v
	}
	if b.DecidedAt != nil {
		v := b.DecidedAt.UTC()
		b.DecidedAt = &v
	}
	return reflect.DeepEqual(a, b)
}

func mediaRequestDecode(raw []byte) (*ports.MediaRequestReceipt, error) {
	var r ports.MediaRequestReceipt
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return nil, ports.ErrUnavailable
	}
	canonical, err := json.Marshal(r)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	var original, roundtrip map[string]any
	if json.Unmarshal(raw, &original) != nil || json.Unmarshal(canonical, &roundtrip) != nil {
		return nil, ports.ErrUnavailable
	}
	o, ok := original["request"].(map[string]any)
	c, canonicalOK := roundtrip["request"].(map[string]any)
	if !ok || !canonicalOK {
		return nil, ports.ErrUnavailable
	}
	for _, key := range []string{"created_at", "updated_at", "decided_at"} {
		value, exists := o[key]
		if !exists {
			continue
		}
		s, valid := value.(string)
		expected, present := c[key].(string)
		actual, parseErr := time.Parse(time.RFC3339Nano, s)
		want, wantErr := time.Parse(time.RFC3339Nano, expected)
		if !valid || !present || parseErr != nil || wantErr != nil || !actual.Equal(want) {
			return nil, ports.ErrUnavailable
		}
		o[key] = expected
	}
	if !reflect.DeepEqual(original, roundtrip) {
		return nil, ports.ErrUnavailable
	}
	return &r, nil
}

func mediaRequestOriginal(row db.MediaRequest) (*ports.MediaRequestReceipt, error) {
	current, err := mediaRequestDTO(row)
	if err != nil {
		return nil, err
	}
	r, err := mediaRequestDecode(row.CreationReceipt)
	if err != nil {
		return nil, err
	}
	current.State, current.Version, current.UpdatedAt = mediarequest.Pending, 1, current.CreatedAt
	current.DecidedBy, current.DecidedAt, current.SubscriptionID, current.Reason = nil, nil, nil, ""
	if r.CommandKey != row.CreationKey || !mediaRequestEqual(r.Request, current) || row.InputHash != creationDigest([]any{"media.request.submit.v1", current.Intent}) {
		return nil, ports.ErrUnavailable
	}
	return r, nil
}

func mediaRequestPublish(ctx context.Context, q *db.Queries, ids ports.IDGen, actor *ports.Actor, request ports.MediaRequest, event string) error {
	if ids == nil || actor == nil {
		return ports.ErrUnavailable
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: ids.NewUUID(), ActorType: string(actor.Type), ActorID: actor.ID, Action: event, TargetType: "media_request", TargetID: request.ID.String(), Payload: raw}); err != nil {
		return err
	}
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "media_request", AggregateID: request.ID.String()}); err != nil {
		return err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "media_request", AggregateID: request.ID.String()})
	if err != nil {
		return err
	}
	_, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: ids.NewUUID(), AggregateType: "media_request", AggregateID: request.ID.String(), EventType: event, Payload: raw, Seq: int64(seq)})
	return err
}

func (s *MediaRequestStore) begin(ctx context.Context, a *ports.Actor, write bool) (pgx.Tx, *db.Queries, *ports.Actor, error) {
	if err := acquisitionCommandActor(a); err != nil {
		return nil, nil, nil, err
	}
	actor := creationClone(a)
	if s == nil || s.pool == nil || write && (s.ids == nil || s.clock == nil) {
		return nil, nil, nil, ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	q := db.New(tx)
	if err = lockAcquisitionUser(ctx, q, actor); err != nil {
		creationRollback(ctx, tx)
		return nil, nil, nil, err
	}
	return tx, q, actor, nil
}

func mediaRequestRead(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID) (db.MediaRequest, error) {
	row, err := q.GetMediaRequest(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ports.ErrNotFound
	}
	if err == nil && !a.CanAccessUser(row.OwnerID.String()) {
		return row, ports.ErrForbidden
	}
	return row, err
}

func mediaRequestCommandLock(ctx context.Context, q *db.Queries, a *ports.Actor, key string) error {
	raw, _ := json.Marshal([]string{a.ID, key})
	return q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "media_request.command", AggregateID: string(raw)})
}

func mediaRequestReceipt(ctx context.Context, q *db.Queries, a *ports.Actor, key, hash string, id *uuid.UUID) (*ports.MediaRequestReceipt, error) {
	old, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: "user", ActorID: a.ID, IdempotencyKey: "media.request.command:" + key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hash != "" && old.RequestHash != hash {
		return nil, ports.ErrMediaRequestConflict
	}
	r, err := mediaRequestDecode(old.ResponseBody)
	if err != nil {
		return nil, err
	}
	if id != nil && r.Request.ID != *id {
		return nil, ports.ErrMediaRequestConflict
	}
	if old.ID == uuid.Nil || !old.CreatedAt.Valid || old.ActorType != "user" || old.ActorID != a.ID || old.IdempotencyKey != "media.request.command:"+key || r.CommandKey != key || !creationHash(old.RequestHash) || !old.ResourceID.Valid || old.ResourceID.String != r.Request.ID.String() {
		return nil, ports.ErrUnavailable
	}
	row, err := mediaRequestRead(ctx, q, a, r.Request.ID)
	if err != nil {
		return nil, err
	}
	original, err := mediaRequestOriginal(row)
	if err != nil {
		return nil, err
	}
	switch old.ResponseCode {
	case 201:
		if a.ID != row.OwnerID.String() || key != row.CreationKey || old.RequestHash != row.InputHash || !mediaRequestEqual(r.Request, original.Request) {
			return nil, ports.ErrUnavailable
		}
	case 200:
		current, err := mediaRequestDTO(row)
		if err != nil {
			return nil, err
		}
		action := "reject"
		if current.State == mediarequest.Withdrawn {
			action = "withdraw"
		} else if current.State != mediarequest.Rejected {
			return nil, ports.ErrUnavailable
		}
		input := ports.MediaRequestCloseInput{ExpectedVersion: current.Version - 1, Action: action, Reason: current.Reason}
		if current.DecidedBy == nil || current.DecidedBy.String() != a.ID || row.DecisionKey.String != key || row.DecisionHash.String != old.RequestHash || old.RequestHash != creationDigest([]any{"media.request.close.v1", row.ID, input}) || !mediaRequestEqual(r.Request, current) {
			return nil, ports.ErrUnavailable
		}
	default:
		return nil, ports.ErrUnavailable
	}
	return r, nil
}

func mediaRequestIsolation(ctx context.Context, q *db.Queries) error {
	isolated, err := q.GuardMediaScrapeIsolation(ctx)
	if err != nil {
		return err
	}
	if isolated {
		return ports.ErrUnavailable
	}
	return nil
}

func (s *MediaRequestStore) save(ctx context.Context, q *db.Queries, a *ports.Actor, r *ports.MediaRequestReceipt, hash string, code int32, event string) error {
	if err := mediaRequestPublish(ctx, q, s.ids, a, r.Request, event); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: "user", ActorID: a.ID, IdempotencyKey: "media.request.command:" + r.CommandKey, RequestHash: hash, ResponseCode: code, ResourceID: pgtype.Text{String: r.Request.ID.String(), Valid: true}, ResponseBody: raw})
	return err
}

func (s *MediaRequestStore) SubmitMediaRequest(ctx context.Context, a *ports.Actor, in mediarequest.Intent, key string) (out *ports.MediaRequestReceipt, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, q, actor, err := s.begin(ctx, a, true)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	in, err = mediarequest.CanonicalIntent(in)
	if err != nil || !mediaRequestKey(key) {
		return nil, ports.ErrRejected
	}
	owner := uuid.MustParse(actor.ID)
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "media_request.owner", AggregateID: actor.ID}); err != nil {
		return nil, err
	}
	if err = mediaRequestCommandLock(ctx, q, actor, key); err != nil {
		return nil, err
	}
	hash := creationDigest([]any{"media.request.submit.v1", in})
	if original, e := mediaRequestReceipt(ctx, q, actor, key, hash, nil); e != nil || original != nil {
		return original, e
	}
	if _, e := q.GetMediaRequestByKey(ctx, db.GetMediaRequestByKeyParams{OwnerID: owner, CreationKey: key}); !errors.Is(e, pgx.ErrNoRows) {
		if e != nil {
			return nil, e
		}
		return nil, ports.ErrUnavailable
	}
	if err = mediaRequestIsolation(ctx, q); err != nil {
		return nil, err
	}
	count, err := q.CountPendingMediaRequests(ctx, owner)
	if err != nil {
		return nil, err
	}
	if count >= 100 {
		return nil, ports.ErrMediaRequestLimit
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return nil, ports.ErrUnavailable
	}
	out = &ports.MediaRequestReceipt{CommandKey: key, Request: ports.MediaRequest{ID: s.ids.NewUUID(), OwnerID: owner, Intent: in, State: mediarequest.Pending, Version: 1, CreatedAt: now, UpdatedAt: now}}
	if out.Request.ID == uuid.Nil {
		return nil, ports.ErrUnavailable
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	seasons := make([]int32, len(in.Seasons))
	for i, n := range in.Seasons {
		seasons[i] = int32(n)
	}
	row, err := q.CreateMediaRequest(ctx, db.CreateMediaRequestParams{ID: out.Request.ID, OwnerID: owner, MediaType: in.MediaType, TmdbID: in.TMDBID, Seasons: seasons, IncludeSpecials: in.IncludeSpecials, CreatedAt: pgtype.Timestamptz{Time: now, Valid: true}, CreationKey: key, InputHash: hash, CreationReceipt: raw})
	if err != nil {
		return nil, err
	}
	if _, err = mediaRequestOriginal(row); err != nil {
		return nil, err
	}
	if err = s.save(ctx, q, actor, out, hash, 201, "media.request.submitted"); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MediaRequestStore) ReadMediaRequest(ctx context.Context, a *ports.Actor, id uuid.UUID) (out *ports.MediaRequest, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, q, actor, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	row, err := mediaRequestRead(ctx, q, actor, id)
	if err != nil {
		return nil, err
	}
	if _, err = mediaRequestOriginal(row); err != nil {
		return nil, err
	}
	r, err := mediaRequestDTO(row)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *MediaRequestStore) ListMediaRequests(ctx context.Context, a *ports.Actor, limit int, before *uuid.UUID) (out []ports.MediaRequest, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, q, actor, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	if limit < 1 || limit > 100 || before != nil && *before == uuid.Nil {
		return nil, ports.ErrRejected
	}
	cursor := pgtype.UUID{}
	if before != nil {
		id := *before
		if _, err = mediaRequestRead(ctx, q, actor, id); err != nil {
			return nil, err
		}
		cursor = pgtype.UUID{Bytes: id, Valid: true}
	}
	rows, err := q.ListMediaRequests(ctx, db.ListMediaRequestsParams{Admin: actor.Role == ports.RoleAdmin, OwnerID: uuid.MustParse(actor.ID), BeforeID: cursor, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out = make([]ports.MediaRequest, 0, len(rows))
	for _, row := range rows {
		if !actor.CanAccessUser(row.OwnerID.String()) {
			return nil, ports.ErrForbidden
		}
		if _, err = mediaRequestOriginal(row); err != nil {
			return nil, err
		}
		r, e := mediaRequestDTO(row)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *MediaRequestStore) ReadMediaRequestReceipt(ctx context.Context, a *ports.Actor, key string) (out *ports.MediaRequestReceipt, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, q, actor, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	if !mediaRequestKey(key) {
		return nil, ports.ErrRejected
	}
	out, err = mediaRequestReceipt(ctx, q, actor, key, "", nil)
	if err == nil && out == nil {
		return nil, ports.ErrNotFound
	}
	return out, err
}

func (s *MediaRequestStore) CloseMediaRequest(ctx context.Context, a *ports.Actor, id uuid.UUID, in ports.MediaRequestCloseInput, key string) (out *ports.MediaRequestReceipt, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, q, actor, err := s.begin(ctx, a, true)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	if id == uuid.Nil || in.ExpectedVersion < 1 || !mediaRequestKey(key) || in.Action != "reject" && in.Action != "withdraw" {
		return nil, ports.ErrRejected
	}
	if in.Action == "reject" && actor.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	in.Reason, err = mediarequest.CanonicalReason(in.Reason)
	if err != nil {
		return nil, ports.ErrRejected
	}
	row, err := q.LockMediaRequest(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !actor.CanAccessUser(row.OwnerID.String()) || in.Action == "withdraw" && actor.ID != row.OwnerID.String() {
		return nil, ports.ErrForbidden
	}
	if err = mediaRequestCommandLock(ctx, q, actor, key); err != nil {
		return nil, err
	}
	hash := creationDigest([]any{"media.request.close.v1", id, in})
	if original, e := mediaRequestReceipt(ctx, q, actor, key, hash, &id); e != nil || original != nil {
		return original, e
	}
	if _, err = mediaRequestOriginal(row); err != nil {
		return nil, err
	}
	if row.State != mediarequest.Pending || row.Version != in.ExpectedVersion {
		return nil, ports.ErrMediaRequestConflict
	}
	if err = mediaRequestIsolation(ctx, q); err != nil {
		return nil, err
	}
	state, _ := mediarequest.Transition(row.State, in.Action)
	row, err = q.CloseMediaRequest(ctx, db.CloseMediaRequestParams{State: state, DecisionActorID: uuid.MustParse(actor.ID), Reason: in.Reason, DecisionKey: key, DecisionHash: hash, RequestID: id, ExpectedVersion: in.ExpectedVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrMediaRequestConflict
	}
	if err != nil {
		return nil, err
	}
	r, err := mediaRequestDTO(row)
	if err != nil {
		return nil, err
	}
	out = &ports.MediaRequestReceipt{CommandKey: key, Request: r}
	if err = s.save(ctx, q, actor, out, hash, 200, "media.request."+state); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
