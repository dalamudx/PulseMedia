package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LibraryScanRuntimeStore struct {
	pool *pgxpool.Pool
	gen  ports.IDGen
}

func NewLibraryScanRuntimeStore(pool *pgxpool.Pool, gen ports.IDGen) *LibraryScanRuntimeStore {
	return &LibraryScanRuntimeStore{pool: pool, gen: gen}
}
func (s *LibraryScanRuntimeStore) QueueDue(ctx context.Context, limit int32) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	if err := db.New(s.pool).InitializeLibraryScanSchedules(ctx); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	roots, err := q.LockDueLibraryScanRoots(ctx, limit)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, root := range roots {
		due, err := q.IsLibraryScanRootDue(ctx, root)
		if err != nil {
			return 0, err
		}
		if !due {
			continue
		}
		job := s.gen.NewUUID()
		if err = q.InsertLibraryScanJob(ctx, db.InsertLibraryScanJobParams{JobID: job, RootID: root}); err != nil {
			return 0, err
		}
		if err = q.SetLibraryScheduledJob(ctx, db.SetLibraryScheduledJobParams{JobID: pgtype.UUID{Bytes: job, Valid: true}, RootID: root}); err != nil {
			return 0, err
		}
		queued++
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return queued, nil
}
func (s *LibraryScanRuntimeStore) Work(ctx context.Context, lease ports.JobLease) (ports.LibraryScanWork, error) {
	r, err := db.New(s.pool).GetLibraryScanWork(ctx, lease.ID)
	if err != nil {
		return ports.LibraryScanWork{}, scanRootError(err)
	}
	work := ports.LibraryScanWork{Lease: lease, RootID: r.RootID, Phase: r.Phase}
	if r.RunID.Valid {
		work.RunID = r.RunID.Bytes
	}
	return work, nil
}
func NewLeasedScanObservationStore(pool *pgxpool.Pool, gen ports.IDGen, work ports.LibraryScanWork) *ScanObservationStore {
	return &ScanObservationStore{pool: pool, work: &work, gen: gen}
}
func (s *ScanObservationStore) guardWork(ctx context.Context, q *db.Queries, root uuid.UUID, phase string) error {
	if s.work == nil {
		return nil
	}
	w := s.work
	if w.RootID != root || w.Phase != phase {
		return ports.ErrScanObservationStale
	}
	_, err := q.GuardOwnedLibraryScanWork(ctx, db.GuardOwnedLibraryScanWorkParams{JobID: w.Lease.ID, RootID: root, Phase: phase, Owner: w.Lease.Owner, Token: w.Lease.Token})
	return scanRootError(err)
}
func (s *ScanObservationStore) bindWorkRun(ctx context.Context, q *db.Queries, run uuid.UUID) error {
	if s.work == nil {
		return nil
	}
	n, err := q.BindLibraryScanRun(ctx, db.BindLibraryScanRunParams{JobID: s.work.Lease.ID, RunID: run})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrScanObservationStale
	}
	return nil
}
func (s *ScanObservationStore) guardRun(ctx context.Context, q *db.Queries, root, run uuid.UUID, phase string) error {
	if err := s.guardWork(ctx, q, root, phase); err != nil {
		return err
	}
	if s.work == nil {
		return nil
	}
	w, err := q.GetLibraryScanWork(ctx, s.work.Lease.ID)
	if err != nil {
		return scanRootError(err)
	}
	if w.RootID != root || w.Phase != phase || !w.RunID.Valid || uuid.UUID(w.RunID.Bytes) != run {
		return ports.ErrScanObservationStale
	}
	return nil
}
func (s *ScanObservationStore) commitWork(ctx context.Context, tx pgx.Tx, root uuid.UUID, phase string) error {
	if err := s.guardWork(ctx, db.New(tx), root, phase); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *ScanObservationStore) finishWork(ctx context.Context, q *db.Queries, root, run uuid.UUID, phase string, review bool) error {
	if s.work == nil {
		return nil
	}
	if err := s.guardRun(ctx, q, root, run, phase); err != nil {
		return err
	}
	result, err := json.Marshal(struct {
		RunID  uuid.UUID `json:"run_id"`
		Review bool      `json:"review_required"`
	}{run, review})
	if err != nil {
		return err
	}
	w := s.work
	if _, err = q.FinishJob(ctx, db.FinishJobParams{Column1: w.Lease.ID, Column2: w.Lease.Owner, Column3: w.Lease.Token, Column4: result}); err != nil {
		return scanRootError(err)
	}
	if phase == "scan" {
		if err = q.SetNextLibraryScanDue(ctx, root); err != nil {
			return err
		}
		if !review {
			if err = q.InsertLibraryMissingJob(ctx, db.InsertLibraryMissingJobParams{JobID: s.gen.NewUUID(), RootID: root, RunID: run}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *ScanObservationStore) FinishMissingWork(ctx context.Context) error {
	if s.work == nil || s.work.Phase != "missing" {
		return ports.ErrScanObservationStale
	}
	w := s.work
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if _, err = q.LockAcquisitionRoot(ctx, w.RootID); err != nil {
		return err
	}
	// A last-item transaction may already have completed the task atomically.
	j, err := q.GetJobByID(ctx, w.Lease.ID)
	if err != nil {
		return err
	}
	if j.State == "succeeded" && j.LeaseToken == w.Lease.Token {
		return tx.Commit(ctx)
	}
	pending, err := q.HasQualifiedLibraryMissing(ctx, db.HasQualifiedLibraryMissingParams{RootID: w.RootID, LastRunID: w.RunID})
	if err != nil {
		return err
	}
	if pending {
		return ports.ErrScanObservationStale
	}
	if err = s.finishWork(ctx, q, w.RootID, w.RunID, "missing", false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ ports.LibraryScanSchedulingRepository = (*LibraryScanRuntimeStore)(nil)
