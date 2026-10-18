package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func scanRootSnapshot(r db.LibraryRoot) ports.ScanRootSnapshot {
	return ports.ScanRootSnapshot{ID: r.ID, Path: r.Path, MediaType: r.MediaType, MarkerFilename: r.MarkerFilename, Health: r.Health, Generation: r.Generation, HealthRevision: r.HealthRevision}
}
func scanRootError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrScanObservationStale
	}
	return err
}
func (s *ScanObservationStore) LoadRoot(ctx context.Context, id uuid.UUID) (ports.ScanRootSnapshot, error) {
	q := db.New(s.pool)
	r, err := q.GetLibraryRootByID(ctx, id)
	if err != nil {
		return ports.ScanRootSnapshot{}, scanRootError(err)
	}
	out := scanRootSnapshot(r)
	identity, err := q.GetTrustedScanRootIdentity(ctx, db.GetTrustedScanRootIdentityParams{RootID: id, Generation: r.Generation})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return ports.ScanRootSnapshot{}, err
	}
	out.RootIdentity = &ports.FileIdentity{Device: identity.RootDevice, Inode: identity.RootInode}
	return out, nil
}
func (s *ScanObservationStore) UpdateProbedHealth(ctx context.Context, in ports.ScanRootSnapshot, health string) (ports.ScanRootSnapshot, error) {
	if health != "online" && health != "offline" {
		return ports.ScanRootSnapshot{}, ports.ErrScanObservationStale
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ports.ScanRootSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if _, err = q.LockAcquisitionRoot(ctx, in.ID); err != nil {
		return ports.ScanRootSnapshot{}, err
	}
	if err = s.guardWork(ctx, q, in.ID, "scan"); err != nil {
		return ports.ScanRootSnapshot{}, err
	}
	r, err := q.UpdateProbedRootHealth(ctx, db.UpdateProbedRootHealthParams{RootID: in.ID, Health: health, ExpectedPath: in.Path, ExpectedMediaType: in.MediaType, ExpectedMarkerFilename: in.MarkerFilename, ExpectedGeneration: in.Generation, ExpectedHealthRevision: in.HealthRevision})
	if err != nil {
		return ports.ScanRootSnapshot{}, scanRootError(err)
	}
	if err = s.commitWork(ctx, tx, in.ID, "scan"); err != nil {
		return ports.ScanRootSnapshot{}, err
	}
	return scanRootSnapshot(r), nil
}
func (s *ScanObservationStore) Start(ctx context.Context, in ports.ScanRootSnapshot, runID uuid.UUID) (ports.ScanRunSnapshot, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ports.ScanRunSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if _, err = q.LockAcquisitionRoot(ctx, in.ID); err != nil {
		return ports.ScanRunSnapshot{}, err
	}
	if err = validateTrustedScanRoot(ctx, q, in.ID, in.Generation, in.RootIdentity); err != nil {
		return ports.ScanRunSnapshot{}, err
	}
	if err = s.guardWork(ctx, q, in.ID, "scan"); err != nil {
		return ports.ScanRunSnapshot{}, err
	}
	if s.work != nil {
		w, e := q.GetLibraryScanWork(ctx, s.work.Lease.ID)
		if e != nil {
			return ports.ScanRunSnapshot{}, e
		}
		if w.RunID.Valid {
			old, e := q.LockScanObservationRun(ctx, uuid.UUID(w.RunID.Bytes))
			if e != nil {
				return ports.ScanRunSnapshot{}, e
			}
			if old.RootID != in.ID {
				return ports.ScanRunSnapshot{}, ports.ErrScanObservationStale
			}
			if old.State == "running" {
				if _, e = q.FinishScanRunFailed(ctx, db.FinishScanRunFailedParams{ID: old.ID, State: "partial", Error: pgtype.Text{String: "prior traversal interrupted before recovery", Valid: true}}); e != nil {
					return ports.ScanRunSnapshot{}, e
				}
			}
		}
	}
	r, err := q.StartVerifiedScanRun(ctx, db.StartVerifiedScanRunParams{RootID: in.ID, RunID: runID, ExpectedPath: in.Path, ExpectedMediaType: in.MediaType, ExpectedMarkerFilename: in.MarkerFilename, ExpectedGeneration: in.Generation, ExpectedHealthRevision: in.HealthRevision})
	if err != nil {
		return ports.ScanRunSnapshot{}, scanRootError(err)
	}
	if s.work != nil {
		if err = s.bindWorkRun(ctx, q, runID); err != nil {
			return ports.ScanRunSnapshot{}, err
		}
	}
	if err = s.commitWork(ctx, tx, in.ID, "scan"); err != nil {
		return ports.ScanRunSnapshot{}, err
	}
	return ports.ScanRunSnapshot{Generation: r.Generation, ScanToken: r.ScanToken, StartMutationSeq: r.StartMutationSeq}, nil
}
func (s *ScanObservationStore) Interrupt(ctx context.Context, in ports.ScanSeal) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	root, err := q.LockAcquisitionRoot(ctx, in.RootID)
	if err != nil {
		return scanRootError(err)
	}
	if root.Path != in.Path || root.MediaType != in.MediaType || root.MarkerFilename != in.MarkerFilename || root.Generation != in.Generation || root.HealthRevision != in.HealthRevision || root.Health != "online" || root.ScanToken != in.ScanToken {
		return ports.ErrScanObservationStale
	}
	if err = s.guardRun(ctx, q, in.RootID, in.RunID, "scan"); err != nil {
		return err
	}
	run, err := q.LockScanObservationRun(ctx, in.RunID)
	if err != nil {
		return scanRootError(err)
	}
	if run.RootID != in.RootID || run.State != "running" || run.Generation != in.Generation || run.ScanToken != in.ScanToken || run.StartMutationSeq != in.StartMutationSeq {
		return ports.ErrScanObservationStale
	}
	if _, err = q.FinishScanRunFailed(ctx, db.FinishScanRunFailedParams{ID: in.RunID, State: "partial", Error: pgtype.Text{String: "root scan interrupted", Valid: true}}); err != nil {
		return err
	}
	return s.commitWork(ctx, tx, in.RootID, "scan")
}

// validateTrustedScanRoot runs under the existing root row lock. It never turns
// a failed scan or an independently supplied directory into a trusted baseline.
func validateTrustedScanRoot(ctx context.Context, q *db.Queries, rootID uuid.UUID, generation int64, actual *ports.FileIdentity) error {
	bound, err := q.GetTrustedScanRootIdentity(ctx, db.GetTrustedScanRootIdentityParams{RootID: rootID, Generation: generation})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if actual == nil || actual.Device != bound.RootDevice || actual.Inode != bound.RootInode {
		return ports.ErrScanObservationStale
	}
	return nil
}
