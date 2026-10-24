package postgres

import (
	"context"
	"errors"
	"reflect"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *UntrackedReviewStore) reviewCapture(in ports.UntrackedReviewCapture) (*untrackedReviewCapture, error) {
	c, ok := in.(*untrackedReviewCapture)
	if !ok || c == nil || s == nil || c.store != s {
		return nil, ports.ErrRejected
	}
	if !s.ready() {
		return nil, ports.ErrUnavailable
	}
	return c, nil
}

// terminal is private and is used only after this transaction finishes its own job.
func (s *UntrackedReviewStore) execution(ctx context.Context, tx pgx.Tx, c *untrackedReviewCapture, terminal bool) (db.Job, error) {
	q, x := db.New(tx), c.snapshot
	if err := reviewCurrent(ctx, tx, c.original, true); err != nil {
		return db.Job{}, err
	}
	r, _, err := reviewGet(ctx, q, x.Request.ID, true)
	if err != nil {
		return db.Job{}, err
	}
	if !reviewRowSame(r, c.original) || reviewRequestCanonical(r.Request) != reviewRequestCanonical(x.Request) || !untrackedSame(r.Scope, x.Scope) || !reviewFileSame(r.File, x.File) {
		return db.Job{}, ports.ErrUntrackedReviewStale
	}
	if _, err = q.LockJobForWrite(ctx, x.Lease.ID); err != nil {
		return db.Job{}, err
	}
	var j db.Job
	if terminal {
		j, err = q.GetJobByID(ctx, x.Lease.ID)
	} else {
		j, err = q.GetAuthorizedSearchJob(ctx, checkinLease(x.Lease, ports.UntrackedReviewJobKind))
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ports.ErrUntrackedReviewStale
		}
		return j, err
	}
	rid, err := reviewPayload(j.Payload)
	if err != nil {
		return j, err
	}
	state := "running"
	if terminal {
		state = "succeeded"
	}
	if j.ID != r.JobID || j.Kind != ports.UntrackedReviewJobKind || j.State != state || j.MaxAttempts != 3 || j.Attempts < 1 || j.Attempts > 3 || !j.LeaseOwner.Valid || j.LeaseOwner.String != x.Lease.Owner || j.LeaseToken != x.Lease.Token || !j.LeaseUntil.Valid || rid != r.ID {
		return j, ports.ErrUntrackedReviewStale
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return j, err
	}
	// Lease extensions never refresh the private capture's original DB deadline.
	if c.deadline.IsZero() || !now.Time.Before(c.deadline) || !now.Time.Before(j.LeaseUntil.Time) {
		return j, ports.ErrUntrackedReviewStale
	}
	if terminal && (!j.FinishedAt.Valid || j.Error.Valid) {
		return j, ports.ErrUntrackedReviewStale
	}
	return j, nil
}

func (s *UntrackedReviewStore) LoadUntrackedReview(ctx context.Context, lease ports.JobLease) (out ports.UntrackedReviewCapture, err error) {
	defer func() { err = reviewError(ctx, err) }()
	if !s.ready() {
		return nil, ports.ErrUnavailable
	}
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	// No business row lock precedes the initial lease-authorized payload read.
	j, err := q.GetAuthorizedSearchJob(ctx, checkinLease(lease, ports.UntrackedReviewJobKind))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ports.ErrUntrackedReviewStale
		}
		return nil, err
	}
	id, err := reviewPayload(j.Payload)
	if err != nil {
		return nil, err
	}
	r, _, err := reviewGet(ctx, q, id, false)
	if err != nil {
		return nil, err
	}
	if r.JobID != lease.ID || !j.LeaseUntil.Valid {
		return nil, ports.ErrUntrackedReviewStale
	}
	c := &untrackedReviewCapture{store: s, original: reviewClone(r), snapshot: ports.UntrackedReviewSnapshot{Lease: lease, Request: r.Request, Scope: r.Scope, File: r.File}, deadline: j.LeaseUntil.Time}
	if _, err = s.execution(ctx, tx, c, false); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *UntrackedReviewStore) ValidateUntrackedReview(ctx context.Context, in ports.UntrackedReviewCapture) (err error) {
	defer func() { err = reviewError(ctx, err) }()
	c, err := s.reviewCapture(in)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	if _, err = s.execution(ctx, tx, c, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *UntrackedReviewStore) CompleteUntrackedReview(ctx context.Context, in ports.UntrackedReviewCapture, evidence ports.UntrackedReviewEvidence) (err error) {
	defer func() { err = reviewError(ctx, err) }()
	c, err := s.reviewCapture(in)
	if err != nil {
		return err
	}
	// Validate before cloning so even rejected input allocations remain bounded.
	if !reviewEvidenceValid(c.snapshot.Request.Input, evidence) {
		return ports.ErrRejected
	}
	evidence = reviewClone(evidence)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if _, err = s.execution(ctx, tx, c, false); err != nil {
		return err
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return err
	}
	recorded := now.Time
	result := ports.UntrackedReview{Request: c.original.Request, State: reviewEvidenceState(&evidence), Evidence: &evidence, MaterialHash: reviewMaterialHash(c.original, evidence), RecordedAt: &recorded, ExecutionAllowed: false}
	if err = q.InsertUntrackedReviewResult(ctx, db.InsertUntrackedReviewResultParams{RequestID: c.original.ID, MaterialHash: result.MaterialHash, Review: reviewJSON(result), RecordedAt: now}); err != nil {
		return err
	}
	n, err := q.FinishUntrackedReviewJob(ctx, db.FinishUntrackedReviewJobParams{ID: c.snapshot.Lease.ID, Owner: c.snapshot.Lease.Owner, Token: c.snapshot.Lease.Token, Result: reviewJSON(result)})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrUntrackedReviewStale
	}
	aid, eid, err := s.audit(ctx, q, discoveryActor(c.original.OwnerID), c.original.ID, "untracked.review.completed", reviewJSON(result))
	if err != nil {
		return err
	}
	recheck := func() error {
		if _, e := s.execution(ctx, tx, c, true); e != nil {
			return e
		}
		actual, e := reviewResult(ctx, q, c.original)
		if e != nil {
			return e
		}
		if !reviewJSONSame(reviewJSON(actual), reviewJSON(result)) {
			return ports.ErrUntrackedReviewStale
		}
		return nil
	}
	if err = recheck(); err != nil {
		return err
	}
	if err = reviewTail(ctx, tx, c.original.ID, aid, eid, recheck); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *UntrackedReviewStore) RecoverUntrackedReviews(ctx context.Context, limit int) (count int, err error) {
	defer func() { err = reviewError(ctx, err) }()
	if !s.ready() {
		return 0, ports.ErrUnavailable
	}
	n, err := discoveryLimit(limit)
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = watchGuard(ctx, q); err != nil {
		return 0, err
	}
	ids, err := q.RecoverUntrackedReviewJobs(ctx, int32(n))
	if err != nil {
		return 0, err
	}
	before := make([]db.Job, 0, len(ids))
	for _, id := range ids {
		j, e := q.GetJobByID(ctx, id)
		if e != nil {
			return 0, e
		}
		if j.Kind != ports.UntrackedReviewJobKind || j.MaxAttempts != 3 || (j.State != "queued" && j.State != "dead") || j.LeaseOwner.Valid || j.LeaseUntil.Valid {
			return 0, ports.ErrUntrackedReviewStale
		}
		before = append(before, j)
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return 0, err
	}
	if err = watchGuard(ctx, q); err != nil {
		return 0, err
	}
	for i, id := range ids {
		after, e := q.GetJobByID(ctx, id)
		if e != nil {
			return 0, e
		}
		if !reflect.DeepEqual(before[i], after) {
			return 0, ports.ErrUntrackedReviewStale
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}
