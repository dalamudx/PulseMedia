package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/library"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *ScanObservationStore) FindFinalizing(ctx context.Context, rootID uuid.UUID) (*ports.ScanSeal, error) {
	f, err := db.New(s.pool).GetCurrentScanFinalization(ctx, rootID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ports.ScanSeal{RunID: f.ScanRunID, RootID: f.RootID, Path: f.ExpectedPath, MediaType: f.ExpectedMediaType, MarkerFilename: f.ExpectedMarkerFilename, HealthRevision: f.ExpectedHealthRevision, Generation: f.Generation, ScanToken: f.ScanToken, StartMutationSeq: f.StartMutationSeq, RootIdentity: ports.FileIdentity{Device: f.RootDevice, Inode: f.RootInode}}, nil
}

func lockScanSeal(ctx context.Context, q *db.Queries, in ports.ScanSeal) (db.ScanRun, error) {
	root, err := q.LockAcquisitionRoot(ctx, in.RootID)
	if err != nil {
		return db.ScanRun{}, err
	}
	if root.Path != in.Path || root.MediaType != in.MediaType || root.MarkerFilename != in.MarkerFilename || root.Generation != in.Generation || root.ScanToken != in.ScanToken || root.Health != "online" || in.RootIdentity.Inode <= 0 {
		return db.ScanRun{}, ports.ErrScanObservationStale
	}
	if err := validateTrustedScanRoot(ctx, q, in.RootID, in.Generation, &in.RootIdentity); err != nil {
		return db.ScanRun{}, err
	}
	run, err := q.LockScanObservationRun(ctx, in.RunID)
	if err != nil {
		return run, err
	}
	if run.RootID != in.RootID || run.Generation != in.Generation || run.ScanToken != in.ScanToken || run.StartMutationSeq != in.StartMutationSeq {
		return run, ports.ErrScanObservationStale
	}
	// Publishing the baseline can advance health_revision. A completed receipt
	// remains read-only and must still match its immutable stored seal.
	if run.State != "succeeded" && root.HealthRevision != in.HealthRevision {
		return run, ports.ErrScanObservationStale
	}
	return run, nil
}
func sameScanSeal(f db.ScanFinalization, in ports.ScanSeal) bool {
	return f.RootID == in.RootID && f.ExpectedPath == in.Path && f.ExpectedMediaType == in.MediaType && f.ExpectedMarkerFilename == in.MarkerFilename && f.ExpectedHealthRevision == in.HealthRevision && f.RootDevice == in.RootIdentity.Device && f.RootInode == in.RootIdentity.Inode
}

func (s *ScanObservationStore) lockWorkSeal(ctx context.Context, q *db.Queries, in ports.ScanSeal, phase string) (db.ScanRun, error) {
	run, err := lockScanSeal(ctx, q, in)
	if err != nil {
		return run, err
	}
	if s.work != nil && phase == "scan" && run.State == "finalizing" {
		if err = s.guardWork(ctx, q, in.RootID, phase); err != nil {
			return run, err
		}
		if err = s.bindWorkRun(ctx, q, in.RunID); err != nil {
			return run, err
		}
	}
	if err = s.guardRun(ctx, q, in.RootID, in.RunID, phase); err != nil {
		return run, err
	}
	if s.work != nil && phase == "missing" && s.work.RunID != in.RunID {
		return run, ports.ErrScanObservationStale
	}
	return run, nil
}
func (s *ScanObservationStore) Seal(ctx context.Context, in ports.ScanSeal) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	run, err := s.lockWorkSeal(ctx, q, in, "scan")
	if err != nil {
		return err
	}
	if run.State == "finalizing" || run.State == "succeeded" {
		f, e := q.LockScanFinalization(ctx, in.RunID)
		if e != nil {
			return e
		}
		if !sameScanSeal(f, in) {
			return ports.ErrScanObservationStale
		}
		if err = recordScanCapacity(ctx, q, in, true); err != nil {
			return err
		}
		return s.commitWork(ctx, tx, in.RootID, "scan")
	}
	if run.State != "running" {
		return ports.ErrScanObservationStale
	}
	if s.work != nil {
		if err = s.bindWorkRun(ctx, q, in.RunID); err != nil {
			return err
		}
	}
	if err = q.InsertScanFinalization(ctx, db.InsertScanFinalizationParams{ScanRunID: in.RunID, RootID: in.RootID, ExpectedPath: in.Path, ExpectedMediaType: in.MediaType, ExpectedMarkerFilename: in.MarkerFilename, ExpectedHealthRevision: in.HealthRevision, RootDevice: in.RootIdentity.Device, RootInode: in.RootIdentity.Inode}); err != nil {
		return err
	}
	if err = recordScanCapacity(ctx, q, in, false); err != nil {
		return err
	}
	if err = q.SealScanRun(ctx, in.RunID); err != nil {
		return err
	}
	return s.commitWork(ctx, tx, in.RootID, "scan")
}

func (s *ScanObservationStore) ReconcileBatch(ctx context.Context, in ports.ScanSeal) (ports.ScanFinalizationProgress, error) {
	var result ports.ScanFinalizationProgress
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	run, err := s.lockWorkSeal(ctx, q, in, "scan")
	if err != nil {
		return result, err
	}
	f, err := q.LockScanFinalization(ctx, in.RunID)
	if err != nil {
		return result, err
	}
	if !sameScanSeal(f, in) {
		return result, ports.ErrScanObservationStale
	}
	result = ports.ScanFinalizationProgress{RunID: in.RunID, Observed: run.FilesObserved, Added: run.FilesAdded, Updated: run.FilesUpdated, Missing: run.FilesMissingCandidates, MissingReviewRequired: f.MissingReviewRequired}
	if run.State == "succeeded" && f.Exhausted && f.CompletedAt.Valid {
		result.Done = true
		return result, s.commitWork(ctx, tx, in.RootID, "scan")
	}
	if run.State != "finalizing" || f.CompletedAt.Valid {
		return result, ports.ErrScanObservationStale
	}
	page, err := q.ListScanReconciliationPage(ctx, db.ListScanReconciliationPageParams{RunID: in.RunID, CursorFileID: f.CursorFileID})
	if err != nil {
		return result, err
	}
	var missing int64
	cursor := f.CursorFileID
	for _, file := range page {
		cursor = pgtype.UUID{Bytes: file.ID, Valid: true}
		if file.Unseen {
			if err = q.ClearSupersededMissingCandidate(ctx, db.ClearSupersededMissingCandidateParams{FileID: file.ID, RunID: in.RunID}); err != nil {
				return ports.ScanFinalizationProgress{}, err
			}
			if err = q.RecordScanMissingCandidate(ctx, db.RecordScanMissingCandidateParams{FileID: file.ID, RunID: in.RunID}); err != nil {
				return ports.ScanFinalizationProgress{}, err
			}
			missing++
		}
	}
	done := len(page) < 128
	if err = q.AddScanMissingCount(ctx, db.AddScanMissingCountParams{ID: in.RunID, FilesMissingCandidates: missing}); err != nil {
		return ports.ScanFinalizationProgress{}, err
	}
	if err = q.AdvanceScanFinalization(ctx, db.AdvanceScanFinalizationParams{ScanRunID: in.RunID, CursorFileID: cursor, Exhausted: done}); err != nil {
		return ports.ScanFinalizationProgress{}, err
	}
	if done {
		result.MissingReviewRequired = library.MissingReviewRequired(result.Missing+missing, f.IndexedFiles)
		if err = q.SetScanMissingReview(ctx, db.SetScanMissingReviewParams{ScanRunID: in.RunID, MissingReviewRequired: result.MissingReviewRequired}); err != nil {
			return ports.ScanFinalizationProgress{}, err
		}
		if err = q.CompleteFinalizedScan(ctx, in.RunID); err != nil {
			return ports.ScanFinalizationProgress{}, err
		}
		if err = q.CompleteScanFinalization(ctx, in.RunID); err != nil {
			return ports.ScanFinalizationProgress{}, err
		}
		if err = q.PublishScanBaseline(ctx, in.RootID); err != nil {
			return ports.ScanFinalizationProgress{}, err
		}
		if err = q.RecordScanCompletedHealthRevision(ctx, in.RunID); err != nil {
			return ports.ScanFinalizationProgress{}, err
		}
		if s.work != nil {
			if err = s.finishWork(ctx, q, in.RootID, in.RunID, "scan", result.MissingReviewRequired); err != nil {
				return ports.ScanFinalizationProgress{}, err
			}
			if err = tx.Commit(ctx); err != nil {
				return ports.ScanFinalizationProgress{}, err
			}
			result.Missing += missing
			result.Done = true
			return result, nil
		}
	}
	if err = s.commitWork(ctx, tx, in.RootID, "scan"); err != nil {
		return ports.ScanFinalizationProgress{}, err
	}
	result.Missing += missing
	result.Done = done
	return result, nil
}

func (s *ScanObservationStore) FailFinalization(ctx context.Context, in ports.ScanSeal) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	run, err := s.lockWorkSeal(ctx, q, in, "scan")
	if err != nil {
		return err
	}
	if run.State != "finalizing" {
		return ports.ErrScanObservationStale
	}
	f, err := q.LockScanFinalization(ctx, in.RunID)
	if err != nil {
		return err
	}
	if !sameScanSeal(f, in) {
		return ports.ErrScanObservationStale
	}
	if _, err = q.UpdateRootHealth(ctx, db.UpdateRootHealthParams{ID: in.RootID, Health: "offline"}); err != nil {
		return err
	}
	if _, err = q.FinishScanRunFailed(ctx, db.FinishScanRunFailedParams{ID: in.RunID, State: "partial", Error: pgtype.Text{String: "finalization root identity unavailable", Valid: true}}); err != nil {
		return err
	}
	return s.commitWork(ctx, tx, in.RootID, "scan")
}

// AbortFinalization retires only the current sealed run when its root stamp
// changed; it must not modify the replacement root's health or baseline.
func (s *ScanObservationStore) AbortFinalization(ctx context.Context, in ports.ScanSeal) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	root, err := q.LockAcquisitionRoot(ctx, in.RootID)
	if err != nil {
		return err
	}
	if err = s.guardWork(ctx, q, in.RootID, "scan"); err != nil {
		return err
	}
	run, err := q.LockScanObservationRun(ctx, in.RunID)
	if err != nil {
		return err
	}
	if root.ScanToken != in.ScanToken || run.RootID != in.RootID || run.Generation != in.Generation || run.ScanToken != in.ScanToken || run.StartMutationSeq != in.StartMutationSeq || run.State != "finalizing" {
		return ports.ErrScanObservationStale
	}
	f, err := q.LockScanFinalization(ctx, in.RunID)
	if err != nil {
		return err
	}
	if !sameScanSeal(f, in) {
		return ports.ErrScanObservationStale
	}
	if _, err = q.FinishScanRunFailed(ctx, db.FinishScanRunFailedParams{ID: in.RunID, State: "partial", Error: pgtype.Text{String: "finalization snapshot changed", Valid: true}}); err != nil {
		return err
	}
	return s.commitWork(ctx, tx, in.RootID, "scan")
}

var _ ports.ScanRepository = (*ScanObservationStore)(nil)
