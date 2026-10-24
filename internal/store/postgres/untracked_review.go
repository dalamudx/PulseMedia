package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UntrackedReviewStore struct {
	pool  *pgxpool.Pool
	ids   ports.IDGen
	clock ports.Clock
}

var (
	_ ports.UntrackedReviewRepository          = (*UntrackedReviewStore)(nil)
	_ ports.UntrackedReviewExecutionRepository = (*UntrackedReviewStore)(nil)
)

func NewUntrackedReviewStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *UntrackedReviewStore {
	return &UntrackedReviewStore{pool: pool, ids: ids, clock: clock}
}

func (s *UntrackedReviewStore) ready() bool {
	return s != nil && s.pool != nil && s.ids != nil && s.clock != nil
}

func reviewReadActor(ctx context.Context, tx pgx.Tx, a *ports.Actor) error {
	if err := discoveryReadGuard(ctx, tx); err != nil {
		return err
	}
	allowed, err := db.New(tx).UntrackedDiscoveryReadAdmin(ctx, uuid.MustParse(a.ID))
	if err != nil {
		return err
	}
	if !allowed {
		return ports.ErrForbidden
	}
	return nil
}

func (s *UntrackedReviewStore) begin(ctx context.Context, a *ports.Actor, read bool) (pgx.Tx, error) {
	if !s.ready() {
		return nil, ports.ErrUnavailable
	}
	opts := pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	if read {
		opts.IsoLevel, opts.AccessMode = pgx.RepeatableRead, pgx.ReadOnly
	}
	tx, err := s.pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if read {
		err = reviewReadActor(ctx, tx, a)
	} else {
		err = untrackedGuard(ctx, db.New(tx), a)
	}
	if err != nil {
		managementRollback(ctx, tx)
		return nil, err
	}
	return tx, nil
}

// Locks retain the existing isolation/account/client/root/scope/file order.
// Receipt recovery uses current ownership, without requiring enabled configuration.
func reviewOwned(ctx context.Context, tx pgx.Tx, owner, scopeID, fileID uuid.UUID, lock bool) (*ports.UntrackedScope, *ports.UntrackedDiscoveredFile, error) {
	c, err := untrackedCurrent(ctx, tx, owner, scopeID, false)
	if err != nil {
		return nil, nil, err
	}
	if c == nil {
		return nil, nil, ports.ErrNotFound
	}
	if lock {
		if err = untrackedLockSelection(ctx, tx, untrackedOldMaterial(*c)); err != nil {
			return nil, nil, err
		}
		locked, e := untrackedCurrent(ctx, tx, owner, scopeID, true)
		if e != nil {
			return nil, nil, e
		}
		if locked == nil || !untrackedSame(*locked, *c) {
			return nil, nil, ports.ErrUntrackedReviewStale
		}
	}
	q := db.New(tx)
	var raw []byte
	if lock {
		raw, err = q.LockUntrackedReviewFile(ctx, fileID)
	} else {
		raw, err = q.GetUntrackedReviewFile(ctx, fileID)
	}
	if err != nil {
		return nil, nil, err
	}
	var identity struct {
		ID      uuid.UUID `json:"id"`
		ScopeID uuid.UUID `json:"scope_id"`
	}
	if json.Unmarshal(raw, &identity) != nil {
		return nil, nil, ports.ErrUnavailable
	}
	if identity.ID != fileID || identity.ScopeID != scopeID {
		return nil, nil, ports.ErrNotFound
	}
	_, f, err := discoveryFile(raw, scopeID, c.Version)
	if err != nil {
		return nil, nil, err
	}
	return c, &f, nil
}

func reviewGet(ctx context.Context, q *db.Queries, id uuid.UUID, lock bool) (untrackedReviewRow, *ports.UntrackedReviewRequestReceipt, error) {
	var raw []byte
	var err error
	if lock {
		raw, err = q.LockUntrackedReviewRequest(ctx, id)
	} else {
		raw, err = q.GetUntrackedReviewRequest(ctx, id)
	}
	if err != nil {
		return untrackedReviewRow{}, nil, err
	}
	return reviewVerifyRequest(ctx, q, raw)
}

func reviewVerifyRequest(ctx context.Context, q *db.Queries, raw []byte) (untrackedReviewRow, *ports.UntrackedReviewRequestReceipt, error) {
	var r untrackedReviewRow
	if checkinDecode(raw, &r) != nil || !reviewRowValid(r) {
		return r, nil, ports.ErrUntrackedReviewStale
	}
	original, err := q.GetUntrackedReviewRequestReceipt(ctx, r.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ports.ErrUntrackedReviewStale
		}
		return r, nil, err
	}
	var receipt ports.UntrackedReviewRequestReceipt
	if checkinDecode(original, &receipt) != nil || receipt.ExecutionAllowed || receipt.CommandKey != r.CommandKey || reviewRequestCanonical(receipt.Request) != reviewRequestCanonical(r.Request) {
		return r, nil, ports.ErrUntrackedReviewStale
	}
	return r, &receipt, nil
}

func reviewRequestKey(ctx context.Context, q *db.Queries, scope, file uuid.UUID, key string) (untrackedReviewRow, *ports.UntrackedReviewRequestReceipt, error) {
	raw, err := q.GetUntrackedReviewRequestKey(ctx, db.GetUntrackedReviewRequestKeyParams{ScopeID: scope, FileID: file, CommandKey: key})
	if err != nil {
		return untrackedReviewRow{}, nil, err
	}
	r, receipt, err := reviewVerifyRequest(ctx, q, raw)
	if err == nil && (r.ScopeID != scope || r.FileID != file || r.CommandKey != key) {
		err = ports.ErrUntrackedReviewStale
	}
	return r, receipt, err
}

func reviewCurrent(ctx context.Context, tx pgx.Tx, r untrackedReviewRow, lock bool) error {
	if err := untrackedGuard(ctx, db.New(tx), discoveryActor(r.OwnerID)); err != nil {
		return err
	}
	c, f, err := reviewOwned(ctx, tx, r.OwnerID, r.ScopeID, r.FileID, lock)
	if err != nil {
		return err
	}
	if !untrackedSame(*c, r.Scope) || !reviewFileSame(*f, r.File) {
		return ports.ErrUntrackedReviewStale
	}
	if _, err = discoveryMaterial(ctx, db.New(tx), *c); err != nil {
		return err
	}
	return nil
}

func reviewJSONSame(a, b []byte) bool {
	var x, y any
	left, right := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	left.UseNumber()
	right.UseNumber()
	return left.Decode(&x) == nil && right.Decode(&y) == nil && reflect.DeepEqual(x, y)
}

func (s *UntrackedReviewStore) audit(ctx context.Context, q *db.Queries, actor *ports.Actor, id uuid.UUID, action string, payload []byte) (uuid.UUID, uuid.UUID, error) {
	if err := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "untracked_review", AggregateID: id.String()}); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	aid, eid := s.ids.NewUUID(), s.ids.NewUUID()
	if aid == uuid.Nil || eid == uuid.Nil || aid == eid {
		return uuid.Nil, uuid.Nil, ports.ErrUnavailable
	}
	a, err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: aid, ActorType: string(actor.Type), ActorID: actor.ID, Action: action, TargetType: "untracked_review", TargetID: id.String(), Payload: payload})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "untracked_review", AggregateID: id.String()})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	e, err := q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: eid, AggregateType: "untracked_review", AggregateID: id.String(), EventType: action, Seq: int64(seq), Payload: payload})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if a.ID != aid || a.ActorType != string(actor.Type) || a.ActorID != actor.ID || a.Action != action || a.TargetType != "untracked_review" || a.TargetID != id.String() || !a.CreatedAt.Valid || !reviewJSONSame(a.Payload, payload) || e.ID != eid || e.AggregateType != "untracked_review" || e.AggregateID != id.String() || e.EventType != action || e.Seq != int64(seq) || !e.CreatedAt.Valid || !reviewJSONSame(e.Payload, payload) {
		return uuid.Nil, uuid.Nil, ports.ErrUntrackedReviewStale
	}
	return aid, eid, nil
}

func reviewTail(ctx context.Context, tx pgx.Tx, id, aid, eid uuid.UUID, recheck func() error) error {
	q := db.New(tx)
	p := db.UntrackedReviewSealParams{RequestID: id, AuditID: aid, EventID: eid}
	before, err := q.UntrackedReviewSeal(ctx, p)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return err
	}
	if err = recheck(); err != nil {
		return err
	}
	after, err := q.UntrackedReviewSeal(ctx, p)
	if err != nil {
		return err
	}
	if !bytes.Equal(before, after) {
		return ports.ErrUntrackedReviewStale
	}
	return recheck()
}

func (s *UntrackedReviewStore) RequestUntrackedReview(ctx context.Context, actor *ports.Actor, scope, file uuid.UUID, in ports.UntrackedReviewInput, key string) (out *ports.UntrackedReviewRequestReceipt, err error) {
	defer func() { err = reviewError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || file == uuid.Nil || !reviewInputValid(in) || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	tx, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	owner, q := uuid.MustParse(a.ID), db.New(tx)
	c, f, err := reviewOwned(ctx, tx, owner, scope, file, true)
	if err != nil {
		return nil, err
	}
	old, receipt, err := reviewRequestKey(ctx, q, scope, file, key)
	if err == nil {
		if old.OwnerID != owner {
			return nil, ports.ErrForbidden
		}
		if old.InputHash != watchDigest(in) {
			return nil, ports.ErrEndpointVersionConflict
		}
		if err = untrackedGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return receipt, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if c.Version != in.ScopeVersion || f.ScopeVersion != c.Version || f.Revision != in.FileRevision {
		return nil, ports.ErrUntrackedReviewStale
	}
	if in.MediaType != c.TargetMediaType {
		return nil, ports.ErrRejected
	}
	if _, err = discoveryMaterial(ctx, q, *c); err != nil {
		return nil, err
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return nil, err
	}
	id, jid := s.ids.NewUUID(), s.ids.NewUUID()
	if id == uuid.Nil || jid == uuid.Nil || id == jid {
		return nil, ports.ErrUnavailable
	}
	request := ports.UntrackedReviewRequest{ID: id, ScopeID: scope, FileID: file, ActorID: owner, Input: in, JobID: jid, CreatedAt: now.Time}
	expected := untrackedReviewRow{ID: id, OwnerID: owner, ScopeID: scope, FileID: file, JobID: jid, CommandKey: key, InputHash: watchDigest(in), Request: request, Scope: *c, File: *f}
	out = &ports.UntrackedReviewRequestReceipt{CommandKey: key, Request: request, ExecutionAllowed: false}
	if err = q.CreateUntrackedReviewJob(ctx, db.CreateUntrackedReviewJobParams{ID: jid, RequestID: id}); err != nil {
		return nil, err
	}
	if err = q.InsertUntrackedReviewRequest(ctx, db.InsertUntrackedReviewRequestParams{ID: id, OwnerID: owner, ScopeID: scope, FileID: file, JobID: jid, CommandKey: key, InputHash: expected.InputHash, Request: reviewJSON(request), ScopeSnapshot: reviewJSON(*c), FileSnapshot: reviewJSON(*f)}); err != nil {
		return nil, err
	}
	if err = q.InsertUntrackedReviewRequestReceipt(ctx, db.InsertUntrackedReviewRequestReceiptParams{RequestID: id, Receipt: reviewJSON(out)}); err != nil {
		return nil, err
	}
	aid, eid, err := s.audit(ctx, q, a, id, "untracked.review.requested", reviewJSON(out))
	if err != nil {
		return nil, err
	}
	recheck := func() error {
		if e := reviewCurrent(ctx, tx, expected, false); e != nil {
			return e
		}
		actual, _, e := reviewGet(ctx, q, id, false)
		if e != nil {
			return e
		}
		if !reviewRowSame(actual, expected) {
			return ports.ErrUntrackedReviewStale
		}
		j, e := q.GetJobByID(ctx, jid)
		if e != nil {
			return e
		}
		rid, e := reviewPayload(j.Payload)
		if e != nil {
			return e
		}
		if j.ID != jid || j.Kind != ports.UntrackedReviewJobKind || j.State != "queued" || j.MaxAttempts != 3 || j.Attempts != 0 || rid != id || !j.DedupeKey.Valid || j.DedupeKey.String != jid.String() {
			return ports.ErrUntrackedReviewStale
		}
		return nil
	}
	if err = recheck(); err != nil {
		return nil, err
	}
	if err = reviewTail(ctx, tx, id, aid, eid, recheck); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UntrackedReviewStore) ReadUntrackedReviewRequestReceipt(ctx context.Context, actor *ports.Actor, scope, file uuid.UUID, key string) (out *ports.UntrackedReviewRequestReceipt, err error) {
	defer func() { err = reviewError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || file == uuid.Nil || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	tx, err := s.begin(ctx, a, true)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	if _, _, err = reviewOwned(ctx, tx, uuid.MustParse(a.ID), scope, file, false); err != nil {
		return nil, err
	}
	r, out, err := reviewRequestKey(ctx, db.New(tx), scope, file, key)
	if err != nil {
		return nil, err
	}
	if r.OwnerID.String() != a.ID {
		return nil, ports.ErrForbidden
	}
	if err = reviewReadActor(ctx, tx, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// A missing result is a pending/terminal job state, never invented evidence.
func reviewResult(ctx context.Context, q *db.Queries, r untrackedReviewRow) (*ports.UntrackedReview, error) {
	raw, err := q.GetUntrackedReviewResult(ctx, r.ID)
	j, e := q.GetJobByID(ctx, r.JobID)
	if e != nil {
		return nil, e
	}
	rid, e := reviewPayload(j.Payload)
	if e != nil {
		return nil, e
	}
	if rid != r.ID || j.Kind != ports.UntrackedReviewJobKind || j.MaxAttempts != 3 {
		return nil, ports.ErrUntrackedReviewStale
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if j.State == "succeeded" {
			return nil, ports.ErrUntrackedReviewStale
		}
		return &ports.UntrackedReview{Request: r.Request, State: j.State, ExecutionAllowed: false}, nil
	}
	if err != nil {
		return nil, err
	}
	var row struct {
		RequestID    uuid.UUID             `json:"request_id"`
		MaterialHash string                `json:"material_hash"`
		Review       ports.UntrackedReview `json:"review"`
		RecordedAt   time.Time             `json:"recorded_at"`
	}
	if checkinDecode(raw, &row) != nil {
		return nil, ports.ErrUntrackedReviewStale
	}
	v := row.Review
	if row.RequestID != r.ID || reviewRequestCanonical(v.Request) != reviewRequestCanonical(r.Request) || v.State != reviewEvidenceState(v.Evidence) || j.State != "succeeded" || v.Stale || v.ExecutionAllowed || v.Evidence == nil || !reviewEvidenceValid(r.Request.Input, *v.Evidence) || v.RecordedAt == nil || !v.RecordedAt.Equal(row.RecordedAt) || row.RecordedAt.IsZero() || !reviewHashValid(v.MaterialHash) || v.MaterialHash != row.MaterialHash || v.MaterialHash != reviewMaterialHash(r, *v.Evidence) || !reviewJSONSame(j.Result, reviewJSON(v)) {
		return nil, ports.ErrUntrackedReviewStale
	}
	return &v, nil
}

func (s *UntrackedReviewStore) ReadUntrackedReview(ctx context.Context, actor *ports.Actor, scope, file, id uuid.UUID) (out *ports.UntrackedReview, err error) {
	defer func() { err = reviewError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || file == uuid.Nil || id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	tx, err := s.begin(ctx, a, true)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	c, f, err := reviewOwned(ctx, tx, uuid.MustParse(a.ID), scope, file, false)
	if err != nil {
		return nil, err
	}
	r, _, err := reviewGet(ctx, db.New(tx), id, false)
	if err != nil {
		return nil, err
	}
	if r.OwnerID.String() != a.ID || r.ScopeID != scope || r.FileID != file {
		return nil, ports.ErrNotFound
	}
	out, err = reviewResult(ctx, db.New(tx), r)
	if err != nil {
		return nil, err
	}
	out.Stale = !untrackedSame(*c, r.Scope) || !reviewFileSame(*f, r.File)
	if !out.Stale {
		_, e := discoveryMaterial(ctx, db.New(tx), *c)
		if e != nil {
			safe := reviewError(ctx, e)
			if safe != ports.ErrUntrackedReviewStale && safe != ports.ErrRejected && safe != ports.ErrEndpointVersionConflict && safe != ports.ErrNotFound {
				return nil, e
			}
			out.Stale = true
		}
	}
	if err = reviewReadActor(ctx, tx, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
