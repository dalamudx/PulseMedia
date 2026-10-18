package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	transfer "github.com/dalamudx/PulseMedia/internal/adapter/transferlegacy"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

var (
	ErrNoReuseCandidate = errors.New("no qualified file available for cross-root reuse")
	ErrSourceOffline    = errors.New("source library root is offline or unreachable")
)

type ReuseCandidate struct {
	File       db.LibraryFile
	SourceRoot db.LibraryRoot
	Score      int
}

type ReuseService struct {
	pool       *pgxpool.Pool
	idgen      ports.IDGen
	slotLocker *transfer.SlotLocker
	queries    *db.Queries
}

func NewReuseService(pool *pgxpool.Pool, idgen ports.IDGen, locker *transfer.SlotLocker) *ReuseService {
	return &ReuseService{
		pool:       pool,
		idgen:      idgen,
		slotLocker: locker,
		queries:    postgres.Queries(pool),
	}
}

// FindCrossRootCandidate searches other online library roots for an existing qualified file covering the unit.
func (s *ReuseService) FindCrossRootCandidate(ctx context.Context, targetRootID, unitID uuid.UUID, policy quality.Policy) (*ReuseCandidate, error) {
	// Query all active files for this unit
	files, err := s.queries.ListFilesForUnit(ctx, unitID)
	if err != nil {
		return nil, fmt.Errorf("failed to list files for unit: %w", err)
	}

	var bestCandidate *ReuseCandidate

	for _, f := range files {
		// Only consider files in OTHER roots
		if f.RootID == targetRootID || f.RetiredAt.Valid {
			continue
		}

		srcRoot, err := s.queries.GetLibraryRootByID(ctx, f.RootID)
		if err != nil || srcRoot.Health != "online" {
			continue // skip offline roots
		}

		// Read quality facts
		facts, _ := s.queries.ListActiveFactsForUnit(ctx, unitID)
		var prof quality.Profile
		for _, fact := range facts {
			if fact.SourceFileID.Valid && fact.SourceFileID.Bytes == f.ID {
				_ = json.Unmarshal(fact.QualityProfile, &prof)
				break
			}
		}

		score := policy.Score(prof)
		if bestCandidate == nil || score > bestCandidate.Score {
			bestCandidate = &ReuseCandidate{
				File:       f,
				SourceRoot: srcRoot,
				Score:      score,
			}
		}
	}

	if bestCandidate == nil {
		return nil, ErrNoReuseCandidate
	}
	return bestCandidate, nil
}

// ExecuteCrossRootReuse transfers a file from a source root to the target root with ordered deadlock-free locking.
func (s *ReuseService) ExecuteCrossRootReuse(
	ctx context.Context,
	opID, sourceRootID, targetRootID, unitID, fileID uuid.UUID,
	movieTitle string,
	year int,
	q quality.Profile,
	strategy string,
) error {
	// 1. Invariant: Multi-root ordered locking (prevents deadlocks between cross-root transfers)
	unlockRoots := s.lockRootsOrdered(sourceRootID, targetRootID)
	defer unlockRoots()

	srcFile, err := s.queries.GetFileByID(ctx, fileID)
	if err != nil {
		return fmt.Errorf("source file not found: %w", err)
	}
	srcRoot, err := s.queries.GetLibraryRootByID(ctx, sourceRootID)
	if err != nil || srcRoot.Health != "online" {
		return ErrSourceOffline
	}
	tgtRoot, err := s.queries.GetLibraryRootByID(ctx, targetRootID)
	if err != nil || tgtRoot.Health != "online" {
		return errors.New("target library root offline")
	}

	sourceFullPath := filepath.Join(srcRoot.Path, srcFile.RelPath)
	ext := filepath.Ext(srcFile.RelPath)
	targetFileName := naming.FormatMovieFile(movieTitle, year, q, ext)
	targetDir := naming.FormatMovieDir(movieTitle, year)
	relPath := filepath.Join(targetDir, targetFileName)

	cleanRel, err := naming.CleanRelPath(relPath)
	if err != nil {
		return fmt.Errorf("target path escapes root: %w", err)
	}
	targetFullPath := filepath.Join(tgtRoot.Path, cleanRel)

	// 2. Physical transfer (hardlink on same device, or verified copy with full SHA-256)
	res, err := transfer.ExecutePhysicalTransfer(sourceFullPath, targetFullPath, strategy)
	if err != nil {
		return fmt.Errorf("physical transfer failed: %w", err)
	}

	srcStat, _ := os.Stat(targetFullPath)

	// 3. In database transaction: register new file, link unit, record imported fact, bump mutation_seq
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin reuse tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	qtx := s.queries.WithTx(tx)

	newFileID := s.idgen.NewUUID()
	newFile, err := qtx.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              newFileID,
		RootID:          targetRootID,
		RelPath:         cleanRel,
		SizeBytes:       res.SizeBytes,
		MtimeNs:         srcStat.ModTime().UnixNano(),
		LastMutationSeq: tgtRoot.MutationSeq + 1,
	})
	if err != nil {
		return fmt.Errorf("failed to create library file: %w", err)
	}

	_ = qtx.LinkFileUnit(ctx, db.LinkFileUnitParams{
		FileID: newFile.ID,
		UnitID: unitID,
	})

	factID := s.idgen.NewUUID()
	qualityBytes, _ := json.Marshal(q)
	_, err = qtx.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             factID,
		UnitID:         unitID,
		Stage:          "imported",
		SourceFileID:   pgtype.UUID{Bytes: newFile.ID, Valid: true},
		RootID:         pgtype.UUID{Bytes: targetRootID, Valid: true},
		QualityProfile: qualityBytes,
	})
	if err != nil {
		return fmt.Errorf("failed to create imported fact: %w", err)
	}

	// Bump mutation_seq on target root to coordinate with scans
	_, _ = qtx.IncrementRootMutationSeq(ctx, targetRootID)

	return tx.Commit(ctx)
}

func (s *ReuseService) lockRootsOrdered(r1, r2 uuid.UUID) func() {
	if bytes.Compare(r1[:], r2[:]) <= 0 {
		u1 := s.slotLocker.LockRoot(r1)
		u2 := s.slotLocker.LockRoot(r2)
		return func() {
			u2()
			u1()
		}
	}
	u1 := s.slotLocker.LockRoot(r2)
	u2 := s.slotLocker.LockRoot(r1)
	return func() {
		u2()
		u1()
	}
}
