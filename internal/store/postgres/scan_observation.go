package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
)

type ScanObservationStore struct {
	pool *pgxpool.Pool
	work *ports.LibraryScanWork
	gen  ports.IDGen
}

func NewScanObservationStore(pool *pgxpool.Pool) *ScanObservationStore {
	return &ScanObservationStore{pool: pool}
}

func (s *ScanObservationStore) Record(ctx context.Context, in ports.ScanObservation) (ports.ScanObservationResult, error) {
	var result ports.ScanObservationResult
	clean, err := naming.CleanRelPath(in.RelPath)
	if err != nil || clean != in.RelPath || in.Identity.Size < 0 {
		return result, ports.ErrScanObservationStale
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	root, err := q.LockAcquisitionRoot(ctx, in.RootID)
	if err != nil {
		return result, err
	}
	if root.Path != in.Path || root.MediaType != in.MediaType || root.MarkerFilename != in.MarkerFilename || root.Generation != in.Generation || root.HealthRevision != in.HealthRevision || root.ScanToken != in.ScanToken || root.Health != "online" {
		return result, ports.ErrScanObservationStale
	}
	if err = s.guardRun(ctx, q, in.RootID, in.RunID, "scan"); err != nil {
		return result, err
	}
	run, err := q.LockScanObservationRun(ctx, in.RunID)
	if err != nil {
		return result, err
	}
	if run.RootID != in.RootID || run.Generation != in.Generation || run.ScanToken != in.ScanToken || run.StartMutationSeq != in.StartMutationSeq || run.State != "running" {
		return result, ports.ErrScanObservationStale
	}
	// The inode is scoped to the verified root identity. Device/mount IDs are not
	// persisted as permanent identity because they can change across a remount.
	fingerprint := pgtype.Text{}
	if in.Identity.Inode > 0 {
		fingerprint = pgtype.Text{String: fmt.Sprintf("stat.v1:%d:%d:%d", in.Identity.Inode, in.Identity.Size, in.Identity.Mtime), Valid: true}
	}
	previous, err := q.GetPreviousScanObservation(ctx, db.GetPreviousScanObservationParams{ScanRunID: in.RunID, RelPath: in.RelPath})
	if err == nil {
		if previous.SizeBytes != in.Identity.Size || previous.MtimeNs != in.Identity.Mtime || previous.Fingerprint != fingerprint {
			return result, ports.ErrScanObservationStale
		}
		return result, s.commitWork(ctx, tx, in.RootID, "scan")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	file, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: in.RootID, RelPath: in.RelPath})
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		return result, err
	}
	changed := !missing && file.LastMutationSeq <= run.StartMutationSeq && (file.SizeBytes != in.Identity.Size || file.MtimeNs != in.Identity.Mtime || (file.FileFingerprint.Valid && strings.HasPrefix(file.FileFingerprint.String, "stat.v1:") && fingerprint.Valid && file.FileFingerprint != fingerprint))
	seq := run.StartMutationSeq
	if changed {
		units, err := q.ListScanFileSourceUnits(ctx, file.ID)
		if err != nil {
			return result, err
		}
		for _, unit := range units {
			if _, err = q.LockAcquisitionUnit(ctx, unit); err != nil {
				return result, err
			}
		}
		locked, lockErr := q.LockScanObservationFile(ctx, db.LockScanObservationFileParams{RootID: in.RootID, RelPath: in.RelPath})
		if lockErr != nil {
			return result, lockErr
		}
		if locked.ID != file.ID || locked.MappingRevision != file.MappingRevision || locked.LastMutationSeq != file.LastMutationSeq || locked.SizeBytes != file.SizeBytes || locked.MtimeNs != file.MtimeNs || locked.FileFingerprint != file.FileFingerprint {
			return result, ports.ErrScanObservationStale
		}
		if err = q.RevokeScanFileSources(ctx, pgtype.UUID{Bytes: file.ID, Valid: true}); err != nil {
			return result, err
		}
		if err = q.RetireScanFileMappings(ctx, file.ID); err != nil {
			return result, err
		}
		if err = q.RetireFileByID(ctx, file.ID); err != nil {
			return result, err
		}
		if _, err = q.IncrementRootMutationSeq(ctx, in.RootID); err != nil {
			return result, err
		}
		seq = root.MutationSeq + 1
	}
	if missing || changed {
		if _, err = q.CreateObservedLibraryFile(ctx, db.CreateObservedLibraryFileParams{ID: in.NewFileID, RootID: in.RootID, RelPath: in.RelPath, SizeBytes: in.Identity.Size, MtimeNs: in.Identity.Mtime, FileFingerprint: fingerprint, LastMutationSeq: seq}); err != nil {
			return result, err
		}
		result.Added = missing
		result.Updated = changed
	}
	// Legacy unknown fingerprints remain unknown; observation never re-signs
	// old quality or supplies mappings/facts to the new incarnation.
	if _, err = q.RecordObservation(ctx, db.RecordObservationParams{ID: in.ObservationID, ScanRunID: in.RunID, RootID: in.RootID, RelPath: in.RelPath, SizeBytes: in.Identity.Size, MtimeNs: in.Identity.Mtime, Fingerprint: fingerprint}); err != nil {
		return result, err
	}
	counts := db.AddScanObservationCountsParams{ID: in.RunID}
	if result.Added {
		counts.FilesAdded = 1
	}
	if result.Updated {
		counts.FilesUpdated = 1
	}
	if err = q.AddScanObservationCounts(ctx, counts); err != nil {
		return ports.ScanObservationResult{}, err
	}
	if err = q.ClearObservedMissingCandidates(ctx, db.ClearObservedMissingCandidatesParams{RootID: in.RootID, RelPath: in.RelPath}); err != nil {
		return ports.ScanObservationResult{}, err
	}
	result.Recorded = true
	if err = s.commitWork(ctx, tx, in.RootID, "scan"); err != nil {
		return ports.ScanObservationResult{}, err
	}
	return result, nil
}

var _ ports.ScanObservationWriter = (*ScanObservationStore)(nil)
