package postgres

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type untrackedConfirmationRow struct {
	ID         uuid.UUID                          `json:"id"`
	OwnerID    uuid.UUID                          `json:"owner_id"`
	ScopeID    uuid.UUID                          `json:"scope_id"`
	FileID     uuid.UUID                          `json:"file_id"`
	ReviewID   uuid.UUID                          `json:"review_id"`
	CommandKey string                             `json:"command_key"`
	InputHash  string                             `json:"input_hash"`
	Receipt    ports.UntrackedConfirmationReceipt `json:"receipt"`
}

func reviewConfirmationMatches(r untrackedReviewRow, v *ports.UntrackedReview, in ports.UntrackedConfirmationInput) bool {
	return reviewConfirmationInputValid(in) && v != nil && v.State == "ready" && !v.Stale && !v.ExecutionAllowed && v.Evidence != nil && v.Evidence.Complete && v.Evidence.Candidate != nil && v.Evidence.Reason == "ready" && in.ReviewID == r.ID && in.MaterialHash == v.MaterialHash && in.ScopeVersion == r.Scope.Version && in.FileRevision == r.File.Revision && in.CandidateKey == v.Evidence.Candidate.Key
}

// Historical receipt verification uses only its original immutable review.
func reviewConfirmationKey(ctx context.Context, q *db.Queries, owner, scope, file uuid.UUID, key string) (*ports.UntrackedConfirmationReceipt, string, error) {
	raw, err := q.GetUntrackedConfirmationKey(ctx, db.GetUntrackedConfirmationKeyParams{ScopeID: scope, FileID: file, CommandKey: key})
	if err != nil {
		return nil, "", err
	}
	var row untrackedConfirmationRow
	if checkinDecode(raw, &row) != nil {
		return nil, "", ports.ErrUntrackedReviewStale
	}
	if row.OwnerID != owner {
		return nil, "", ports.ErrForbidden
	}
	receipt := row.Receipt
	m := receipt.Material
	if row.ID == uuid.Nil || row.ScopeID != scope || row.FileID != file || row.CommandKey != key || receipt.CommandKey != key || receipt.State != untrackedConfirmationState || receipt.ExecutionAllowed || m.ID != row.ID || m.ActorID != owner || m.Scope.ID != scope || m.Scope.OwnerID != owner || m.File.ID != file || m.File.ScopeID != scope || m.Input.ReviewID != row.ReviewID || m.CreatedAt.IsZero() || row.InputHash != watchDigest(m.Input) {
		return nil, "", ports.ErrUntrackedReviewStale
	}
	r, _, err := reviewGet(ctx, q, row.ReviewID, false)
	if err != nil {
		return nil, "", err
	}
	v, err := reviewResult(ctx, q, r)
	if err != nil {
		return nil, "", err
	}
	if r.OwnerID != owner || r.ScopeID != scope || r.FileID != file || !reviewConfirmationMatches(r, v, m.Input) || !untrackedSame(m.Scope, r.Scope) || !reviewFileSame(m.File, r.File) || !reviewFileValid(m.File, m.Scope) || !reviewJSONSame(reviewJSON(m.Review), reviewJSON(v)) {
		return nil, "", ports.ErrUntrackedReviewStale
	}
	return &receipt, row.InputHash, nil
}

func (s *UntrackedReviewStore) PrepareUntrackedConfirmation(ctx context.Context, actor *ports.Actor, scope, file uuid.UUID, in ports.UntrackedConfirmationInput, key string) (out *ports.UntrackedConfirmationPreparation, err error) {
	defer func() { err = reviewError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || file == uuid.Nil || !reviewConfirmationInputValid(in) || !doubanSyncASCII(key, 160, false) {
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
	receipt, hash, err := reviewConfirmationKey(ctx, q, owner, scope, file, key)
	if err == nil {
		if hash != watchDigest(in) {
			return nil, ports.ErrEndpointVersionConflict
		}
		if err = untrackedGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return &ports.UntrackedConfirmationPreparation{Receipt: receipt}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	r, _, err := reviewGet(ctx, q, in.ReviewID, true)
	if err != nil {
		return nil, err
	}
	if r.OwnerID != owner || r.ScopeID != scope || r.FileID != file {
		return nil, ports.ErrNotFound
	}
	if !untrackedSame(*c, r.Scope) || !reviewFileSame(*f, r.File) {
		return nil, ports.ErrUntrackedReviewStale
	}
	if _, err = discoveryMaterial(ctx, q, *c); err != nil {
		return nil, err
	}
	if _, err = q.LockJobForWrite(ctx, r.JobID); err != nil {
		return nil, err
	}
	v, err := reviewResult(ctx, q, r)
	if err != nil {
		return nil, err
	}
	if !reviewConfirmationMatches(r, v, in) {
		return nil, ports.ErrUntrackedReviewStale
	}
	issued, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return nil, err
	}
	id := s.ids.NewUUID()
	if id == uuid.Nil {
		return nil, ports.ErrUnavailable
	}
	material := ports.UntrackedConfirmationMaterial{ID: id, ActorID: owner, Scope: r.Scope, File: r.File, Review: *v, Input: in, CreatedAt: issued.Time}
	capture := &untrackedConfirmationCapture{store: s, actor: *a, material: reviewClone(material), key: key, hash: watchDigest(in), deadline: issued.Time.Add(untrackedScopeTTL), issuedLocal: s.clock.Now()}
	if _, err = s.confirmationCurrent(ctx, tx, capture); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ports.UntrackedConfirmationPreparation{Capture: capture}, nil
}

func (s *UntrackedReviewStore) confirmationCurrent(ctx context.Context, tx pgx.Tx, c *untrackedConfirmationCapture) (untrackedReviewRow, error) {
	m, q := c.material, db.New(tx)
	if c.actor.ID != m.ActorID.String() || c.hash != watchDigest(m.Input) || m.ID == uuid.Nil || m.CreatedAt.IsZero() || !c.deadline.Equal(m.CreatedAt.Add(untrackedScopeTTL)) {
		return untrackedReviewRow{}, ports.ErrRejected
	}
	if err := untrackedGuard(ctx, q, &c.actor); err != nil {
		return untrackedReviewRow{}, err
	}
	scope, file, err := reviewOwned(ctx, tx, m.ActorID, m.Scope.ID, m.File.ID, true)
	if err != nil {
		return untrackedReviewRow{}, err
	}
	if !untrackedSame(*scope, m.Scope) || !reviewFileSame(*file, m.File) {
		return untrackedReviewRow{}, ports.ErrUntrackedReviewStale
	}
	if _, err = discoveryMaterial(ctx, q, *scope); err != nil {
		return untrackedReviewRow{}, err
	}
	r, _, err := reviewGet(ctx, q, m.Input.ReviewID, true)
	if err != nil {
		return r, err
	}
	if r.OwnerID != m.ActorID || r.ScopeID != m.Scope.ID || r.FileID != m.File.ID || !untrackedSame(r.Scope, m.Scope) || !reviewFileSame(r.File, m.File) {
		return r, ports.ErrUntrackedReviewStale
	}
	if _, err = q.LockJobForWrite(ctx, r.JobID); err != nil {
		return r, err
	}
	v, err := reviewResult(ctx, q, r)
	if err != nil {
		return r, err
	}
	if err = reviewCanonicalSelection(ctx, tx, r.Request.Input, true); err != nil {
		return r, err
	}
	if !reviewConfirmationMatches(r, v, m.Input) || !reviewJSONSame(reviewJSON(v), reviewJSON(m.Review)) {
		return r, ports.ErrUntrackedReviewStale
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return r, err
	}
	if !now.Time.Before(c.deadline) || s.clock.Since(c.issuedLocal) >= untrackedScopeTTL {
		return r, ports.ErrUntrackedReviewStale
	}
	return r, nil
}

func (s *UntrackedReviewStore) CommitUntrackedConfirmation(ctx context.Context, in ports.UntrackedConfirmationCapture) (out *ports.UntrackedConfirmationReceipt, err error) {
	defer func() { err = reviewError(ctx, err) }()
	c, ok := in.(*untrackedConfirmationCapture)
	if !ok || c == nil || s == nil || c.store != s {
		return nil, ports.ErrRejected
	}
	a, err := reviewActor(&c.actor)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	m, q := c.material, db.New(tx)
	// Same-key recovery remains possible after expiry or later configuration edits.
	if _, _, err = reviewOwned(ctx, tx, m.ActorID, m.Scope.ID, m.File.ID, true); err != nil {
		return nil, err
	}
	old, hash, err := reviewConfirmationKey(ctx, q, m.ActorID, m.Scope.ID, m.File.ID, c.key)
	if err == nil {
		if hash != c.hash || c.hash != watchDigest(m.Input) {
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
	r, err := s.confirmationCurrent(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	out = &ports.UntrackedConfirmationReceipt{CommandKey: c.key, Material: reviewClone(m), State: untrackedConfirmationState, ExecutionAllowed: false}
	if err = q.InsertUntrackedConfirmation(ctx, db.InsertUntrackedConfirmationParams{ID: m.ID, OwnerID: m.ActorID, ScopeID: m.Scope.ID, FileID: m.File.ID, ReviewID: r.ID, CommandKey: c.key, InputHash: c.hash, Receipt: reviewJSON(out)}); err != nil {
		return nil, err
	}
	aid, eid, err := s.audit(ctx, q, a, r.ID, "untracked.review.confirmed", reviewJSON(out))
	if err != nil {
		return nil, err
	}
	recheck := func() error {
		if _, e := s.confirmationCurrent(ctx, tx, c); e != nil {
			return e
		}
		actual, originalHash, e := reviewConfirmationKey(ctx, q, m.ActorID, m.Scope.ID, m.File.ID, c.key)
		if e != nil {
			return e
		}
		if originalHash != c.hash || !reviewJSONSame(reviewJSON(actual), reviewJSON(out)) {
			return ports.ErrUntrackedReviewStale
		}
		return nil
	}
	if err = recheck(); err != nil {
		return nil, err
	}
	if err = reviewTail(ctx, tx, r.ID, aid, eid, recheck); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UntrackedReviewStore) ReadUntrackedConfirmationReceipt(ctx context.Context, actor *ports.Actor, scope, file uuid.UUID, key string) (out *ports.UntrackedConfirmationReceipt, err error) {
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
	out, _, err = reviewConfirmationKey(ctx, db.New(tx), uuid.MustParse(a.ID), scope, file, key)
	if err != nil {
		return nil, err
	}
	if err = reviewReadActor(ctx, tx, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
