package postgres

import (
	"bytes"
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"path"
)

type ExternalBatchStore struct{ review *UntrackedReviewStore }

var _ ports.ExternalBatchRepository = (*ExternalBatchStore)(nil)

func NewExternalBatchStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *ExternalBatchStore {
	return &ExternalBatchStore{review: NewUntrackedReviewStore(pool, ids, clock)}
}

func externalBatchInput(b ports.ExternalBatch) ports.ExternalBatchInput {
	in := ports.ExternalBatchInput{Rows: make([]ports.ExternalBatchSelection, len(b.Rows))}
	for i, r := range b.Rows {
		in.Rows[i] = r.Selection
	}
	return in
}
func externalBatchValid(b ports.ExternalBatch, owner, scope uuid.UUID, key, hash string) bool {
	if b.ID == uuid.Nil || b.ActorID != owner || b.ScopeID != scope || b.CommandKey != key || b.ExecutionAllowed || b.CreatedAt.IsZero() || len(b.Rows) < 1 || len(b.Rows) > ports.ExternalBatchLimit || watchDigest(externalBatchInput(b)) != hash {
		return false
	}
	files, units, targets := map[uuid.UUID]bool{}, map[string]bool{}, map[string]bool{}
	physical := map[[2]int64]bool{}
	first := b.Rows[0]
	work, season, parent := first.Review.Request.Input.TMDBID, first.Review.Request.Input.Season, path.Dir(first.File.RelativePath)
	for _, r := range b.Rows {
		in, c := r.Review.Request.Input, r.Selection.Confirmation
		identity := [2]int64{r.File.Identity.Device, r.File.Identity.Inode}
		if physical[identity] {
			return false
		}
		physical[identity] = true
		if !in.CanonicalSelectionValid() || in.CanonicalUnitID == "" || in.MediaType != "tv" || in.TMDBID != work || in.Season != season || in.MediaMetadataVersion != first.Review.Request.Input.MediaMetadataVersion || !reviewConfirmationInputValid(c) || r.Selection.FileID != r.File.ID || r.Scope.ID != scope || r.Scope.OwnerID != owner || r.Review.Request.ActorID != owner || r.Review.Request.ScopeID != scope || r.Review.Request.FileID != r.File.ID || c.ReviewID != r.Review.Request.ID || !untrackedSame(r.Scope, first.Scope) || r.File.ScopeVersion != r.Scope.Version || r.File.Revision != c.FileRevision || r.Scope.Version != c.ScopeVersion || path.Dir(r.File.RelativePath) != parent || files[r.File.ID] || units[in.CanonicalUnitID] || targets[c.TargetRelativePath] {
			return false
		}
		files[r.File.ID] = true
		units[in.CanonicalUnitID] = true
		targets[c.TargetRelativePath] = true
	}
	return true
}
func (s *ExternalBatchStore) load(ctx context.Context, tx pgx.Tx, a *ports.Actor, scope, id uuid.UUID, key string) (*ports.ExternalBatch, error) {
	q, owner := db.New(tx), uuid.MustParse(a.ID)
	c, err := untrackedCurrent(ctx, tx, owner, scope, false)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ports.ErrNotFound
	}
	var raw []byte
	var hash string
	if id != uuid.Nil {
		v, e := q.GetExternalBatch(ctx, db.GetExternalBatchParams{OwnerID: owner, ScopeID: scope, ID: id})
		err = e
		raw, hash = v.Manifest, v.InputHash
	} else {
		v, e := q.GetExternalBatchCommand(ctx, db.GetExternalBatchCommandParams{OwnerID: owner, ScopeID: scope, CommandKey: key})
		err = e
		raw, hash = v.Manifest, v.InputHash
	}
	if err != nil {
		return nil, err
	}
	var b ports.ExternalBatch
	if checkinDecode(raw, &b) != nil {
		return nil, ports.ErrUnavailable
	}
	if id != uuid.Nil {
		key = b.CommandKey
	}
	if !externalBatchValid(b, owner, scope, key, hash) || id != uuid.Nil && b.ID != id {
		return nil, ports.ErrUntrackedReviewStale
	}
	for _, r := range b.Rows {
		original, _, e := reviewGet(ctx, q, r.Selection.Confirmation.ReviewID, false)
		if e != nil {
			return nil, e
		}
		v, e := reviewResult(ctx, q, original)
		if e != nil {
			return nil, e
		}
		if original.OwnerID != owner || original.ScopeID != scope || original.FileID != r.Selection.FileID || !reviewConfirmationMatches(original, v, r.Selection.Confirmation) || !untrackedSame(original.Scope, r.Scope) || !reviewFileSame(original.File, r.File) || !reviewJSONSame(reviewJSON(v), reviewJSON(r.Review)) {
			return nil, ports.ErrUntrackedReviewStale
		}
	}
	return &b, nil
}
func (s *ExternalBatchStore) ReadExternalBatch(ctx context.Context, actor *ports.Actor, scope, id uuid.UUID, key string) (out *ports.ExternalBatch, err error) {
	defer func() { err = reviewError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || (id == uuid.Nil) == (key == "") || key != "" && !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.review == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.review.begin(ctx, a, true)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	out, err = s.load(ctx, tx, a, scope, id, key)
	if err != nil {
		return nil, err
	}
	if err = reviewReadActor(ctx, tx, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
func (s *ExternalBatchStore) CreateExternalBatch(ctx context.Context, actor *ports.Actor, scope uuid.UUID, in ports.ExternalBatchInput, key string) (out *ports.ExternalBatch, err error) {
	defer func() { err = reviewError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || !doubanSyncASCII(key, 160, false) || len(in.Rows) < 1 || len(in.Rows) > ports.ExternalBatchLimit {
		return nil, ports.ErrRejected
	}
	if s == nil || s.review == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.review.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	owner, q := uuid.MustParse(a.ID), db.New(tx)
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "external_batch_key", AggregateID: a.ID + ":" + scope.String() + ":" + key}); err != nil {
		return nil, err
	}
	old, err := s.load(ctx, tx, a, scope, uuid.Nil, key)
	if err == nil {
		if watchDigest(externalBatchInput(*old)) != watchDigest(in) {
			return nil, ports.ErrEndpointVersionConflict
		}
		if err = untrackedGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return old, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	b := ports.ExternalBatch{ID: s.review.ids.NewUUID(), ActorID: owner, ScopeID: scope, CommandKey: key, Rows: make([]ports.ExternalBatchRow, len(in.Rows))}
	originals := make([]untrackedReviewRow, len(in.Rows))
	for i, row := range in.Rows {
		if row.FileID == uuid.Nil || !reviewConfirmationInputValid(row.Confirmation) {
			return nil, ports.ErrRejected
		}
		r, _, e := reviewGet(ctx, q, row.Confirmation.ReviewID, false)
		if e != nil {
			return nil, e
		}
		if r.OwnerID != owner || r.ScopeID != scope || r.FileID != row.FileID {
			return nil, ports.ErrNotFound
		}
		if e = reviewCurrent(ctx, tx, r, true); e != nil {
			return nil, e
		}
		v, e := reviewResult(ctx, q, r)
		if e != nil {
			return nil, e
		}
		if !reviewConfirmationMatches(r, v, row.Confirmation) {
			return nil, ports.ErrUntrackedReviewStale
		}
		originals[i] = r
		b.Rows[i] = ports.ExternalBatchRow{Selection: row, Scope: r.Scope, File: r.File, Review: *v}
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return nil, err
	}
	b.CreatedAt = now.Time
	hash := watchDigest(in)
	if !externalBatchValid(b, owner, scope, key, hash) {
		return nil, ports.ErrRejected
	}
	raw := reviewJSON(b)
	if err = q.InsertExternalBatch(ctx, db.InsertExternalBatchParams{ID: b.ID, OwnerID: owner, ScopeID: scope, CommandKey: key, InputHash: hash, Manifest: raw, CreatedAt: pgtype.Timestamptz{Time: b.CreatedAt, Valid: true}}); err != nil {
		return nil, err
	}
	if _, _, err = s.review.audit(ctx, q, a, b.ID, "external.batch.previewed", reviewJSON(struct {
		BatchID uuid.UUID `json:"batch_id"`
		Count   int       `json:"count"`
	}{b.ID, len(b.Rows)})); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}
	for _, r := range originals {
		if err = reviewCurrent(ctx, tx, r, false); err != nil {
			return nil, err
		}
	}
	persisted, err := s.load(ctx, tx, a, scope, b.ID, "")
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(reviewJSON(persisted), raw) {
		return nil, ports.ErrUntrackedReviewStale
	}
	if err = untrackedGuard(ctx, q, a); err != nil {
		return nil, err
	}
	return &b, tx.Commit(ctx)
}
