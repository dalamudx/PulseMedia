package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"slices"
)

func missingStale(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrScanObservationStale
	}
	return err
}
func loadMissingSeal(ctx context.Context, q *db.Queries, rootID, runID uuid.UUID) (ports.ScanSeal, error) {
	f, err := q.GetMissingScanSeal(ctx, db.GetMissingScanSealParams{RootID: rootID, ScanRunID: runID})
	if err != nil {
		return ports.ScanSeal{}, missingStale(err)
	}
	run, err := q.LockScanObservationRun(ctx, runID)
	if err != nil {
		return ports.ScanSeal{}, missingStale(err)
	}
	return ports.ScanSeal{RunID: runID, RootID: rootID, Path: f.ExpectedPath, MediaType: f.ExpectedMediaType, MarkerFilename: f.ExpectedMarkerFilename, Generation: run.Generation, ScanToken: run.ScanToken, StartMutationSeq: run.StartMutationSeq, HealthRevision: f.ExpectedHealthRevision, RootIdentity: ports.FileIdentity{Device: f.RootDevice, Inode: f.RootInode}}, nil
}
func (s *ScanObservationStore) LoadMissingScan(ctx context.Context, rootID, runID uuid.UUID) (ports.ScanSeal, error) {
	return loadMissingSeal(ctx, db.New(s.pool), rootID, runID)
}
func (s *ScanObservationStore) ListMissingChecks(ctx context.Context, seal ports.ScanSeal, after uuid.UUID) ([]ports.ScanMissingFile, error) {
	current, err := s.LoadMissingScan(ctx, seal.RootID, seal.RunID)
	if err != nil {
		return nil, err
	}
	if current != seal {
		return nil, ports.ErrScanObservationStale
	}
	rows, err := db.New(s.pool).ListScanMissingToCheck(ctx, db.ListScanMissingToCheckParams{RootID: seal.RootID, RunID: seal.RunID, AfterFileID: pgtype.UUID{Bytes: after, Valid: after != uuid.Nil}})
	if err != nil {
		return nil, err
	}
	out := make([]ports.ScanMissingFile, 0, len(rows))
	for _, r := range rows {
		out = append(out, ports.ScanMissingFile{FileID: r.FileID, FirstRunID: r.FirstRunID, LastRunID: r.LastRunID, RelPath: r.RelPath, LastMutationSeq: r.LastMutationSeq, MappingRevision: r.MappingRevision, Size: r.SizeBytes, Mtime: r.MtimeNs, Fingerprint: r.FileFingerprint.String, FingerprintKnown: r.FileFingerprint.Valid, ValidUntil: r.ValidUntil.Time})
	}
	return out, nil
}
func (s *ScanObservationStore) ResolveMissing(ctx context.Context, seal ports.ScanSeal, in ports.ScanMissingFile, decision ports.ScanMissingDecision) error {
	if (decision != ports.ScanFilePresent && decision != ports.ScanFileMissing) || in.LastRunID != seal.RunID || in.FileID == uuid.Nil || in.FirstRunID == uuid.Nil {
		return ports.ErrScanObservationStale
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	run, err := s.lockWorkSeal(ctx, q, seal, "missing")
	if err != nil {
		return missingStale(err)
	}
	if run.State != "succeeded" {
		return ports.ErrScanObservationStale
	}
	current, err := loadMissingSeal(ctx, q, seal.RootID, seal.RunID)
	if err != nil {
		return err
	}
	if current != seal {
		return ports.ErrScanObservationStale
	}
	fingerprint := pgtype.Text{String: in.Fingerprint, Valid: in.FingerprintKnown}
	receipt, err := q.GetScanMissingReceipt(ctx, db.GetScanMissingReceiptParams{FileID: in.FileID, FirstRunID: in.FirstRunID})
	if err == nil {
		if receipt.LastRunID != in.LastRunID || receipt.RootID != seal.RootID || receipt.Decision != string(decision) || receipt.RelPath != in.RelPath || receipt.LastMutationSeq != in.LastMutationSeq || receipt.MappingRevision != in.MappingRevision || receipt.FileFingerprint != fingerprint || receipt.SizeBytes != in.Size || receipt.MtimeNs != in.Mtime {
			return ports.ErrScanObservationStale
		}
		return s.commitWork(ctx, tx, seal.RootID, "missing")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	units, err := q.ListScanFileSourceUnits(ctx, in.FileID)
	if err != nil {
		return err
	}
	for _, unit := range units {
		if _, err = q.LockAcquisitionUnit(ctx, unit); err != nil {
			return err
		}
	}
	file, err := q.LockScanObservationFile(ctx, db.LockScanObservationFileParams{RootID: seal.RootID, RelPath: in.RelPath})
	if err != nil {
		return missingStale(err)
	}
	if file.ID != in.FileID || file.MappingRevision != in.MappingRevision || file.LastMutationSeq != in.LastMutationSeq || file.FileFingerprint != fingerprint || file.SizeBytes != in.Size || file.MtimeNs != in.Mtime {
		return ports.ErrScanObservationStale
	}
	latestUnits, err := q.ListScanFileSourceUnits(ctx, in.FileID)
	if err != nil {
		return err
	}
	if !slices.Equal(units, latestUnits) {
		return ports.ErrScanObservationStale
	}
	candidate, err := q.LockScanMissingCandidate(ctx, db.LockScanMissingCandidateParams{FileID: in.FileID, FirstRunID: in.FirstRunID})
	if err != nil {
		return missingStale(err)
	}
	if candidate.RootID != seal.RootID || candidate.LastRunID != seal.RunID || candidate.State != "open" {
		return ports.ErrScanObservationStale
	}
	if _, err = q.GetQualifiedScanMissing(ctx, db.GetQualifiedScanMissingParams{FileID: in.FileID, FirstRunID: in.FirstRunID, LastRunID: in.LastRunID}); err != nil {
		return missingStale(err)
	}
	fresh, err := q.MissingCheckStillFresh(ctx, pgtype.Timestamptz{Time: in.ValidUntil, Valid: !in.ValidUntil.IsZero()})
	if err != nil {
		return err
	}
	if !fresh.Valid || !fresh.Bool {
		return ports.ErrScanObservationStale
	}
	if decision == ports.ScanFileMissing {
		if err = q.RevokeMissingFileSources(ctx, pgtype.UUID{Bytes: in.FileID, Valid: true}); err != nil {
			return err
		}
		if err = q.RetireScanFileMappings(ctx, in.FileID); err != nil {
			return err
		}
		if err = q.RetireFileByID(ctx, in.FileID); err != nil {
			return err
		}
		if _, err = q.IncrementRootMutationSeq(ctx, seal.RootID); err != nil {
			return err
		}
	}
	if err = q.CompleteMissingCandidate(ctx, db.CompleteMissingCandidateParams{FileID: in.FileID, FirstRunID: in.FirstRunID, ClearReason: pgtype.Text{String: "physical check: " + string(decision), Valid: true}}); err != nil {
		return err
	}
	if err = q.InsertScanMissingReceipt(ctx, db.InsertScanMissingReceiptParams{FileID: in.FileID, FirstRunID: in.FirstRunID, LastRunID: in.LastRunID, RootID: seal.RootID, Decision: string(decision), RelPath: in.RelPath, LastMutationSeq: in.LastMutationSeq, MappingRevision: in.MappingRevision, FileFingerprint: fingerprint, SizeBytes: in.Size, MtimeNs: in.Mtime}); err != nil {
		return err
	}
	if s.work != nil {
		pending, e := q.HasQualifiedLibraryMissing(ctx, db.HasQualifiedLibraryMissingParams{RootID: seal.RootID, LastRunID: seal.RunID})
		if e != nil {
			return e
		}
		if !pending {
			if err = s.finishWork(ctx, q, seal.RootID, seal.RunID, "missing", false); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
	}
	return s.commitWork(ctx, tx, seal.RootID, "missing")
}

var _ ports.ScanMissingRepository = (*ScanObservationStore)(nil)
