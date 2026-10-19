package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var (
	ErrRootOffline     = errors.New("library root is offline or unreachable")
	ErrMarkerMissing   = errors.New("root marker file is missing (unmounted NAS or disconnected volume protection)")
	ErrScanInterrupted = errors.New("scan interrupted by directory access error (partial scan protection)")
)

type ScanReport struct {
	ScanRunID             uuid.UUID
	RootID                uuid.UUID
	FilesObserved         int64
	FilesAdded            int64
	FilesUpdated          int64
	MissingCandidates     int64
	MissingReviewRequired bool
	State                 string
}

type Scanner struct {
	idgen        ports.IDGen
	files        ports.ScanFilesystem
	observations ports.ScanRepository
	capacity     ports.RootCapacitySampler
}

func NewScanner(idgen ports.IDGen, files ports.ScanFilesystem, observations ports.ScanRepository) *Scanner {
	return &Scanner{idgen: idgen, files: files, observations: observations}
}

// WithCapacitySampler configures the optional display observer before execution.
func (s *Scanner) WithCapacitySampler(sampler ports.RootCapacitySampler) *Scanner {
	s.capacity = sampler
	return s
}

// ProbeRoot verifies that the directory exists and its identity marker file is present.
func (s *Scanner) ProbeRoot(rootPath, markerFilename string) error {
	info, err := os.Stat(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: path does not exist (%s)", ErrRootOffline, rootPath)
		}
		return fmt.Errorf("%w: unable to stat path: %v", ErrRootOffline, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: path is not a directory (%s)", ErrRootOffline, rootPath)
	}

	if markerFilename == "" {
		markerFilename = ".pulsemedia_root"
	}
	markerPath := filepath.Join(rootPath, markerFilename)
	if _, err := os.Stat(markerPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s not found in %s", ErrMarkerMissing, markerFilename, rootPath)
		}
		return fmt.Errorf("%w: unable to stat marker: %v", ErrRootOffline, err)
	}

	return nil
}

// ScanRoot executes a complete library scan run with mount probe and mutation gates.
func (s *Scanner) ScanRoot(ctx context.Context, rootID uuid.UUID) (*ScanReport, error) {
	root, err := s.observations.LoadRoot(ctx, rootID)
	if err != nil {
		return nil, fmt.Errorf("failed to load library root: %w", err)
	}

	// Scans use the identical non-expiring physical slot as transfers.
	roots, release, err := s.files.LockRoots(ctx, root.Path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrRootOffline
	}
	defer release()
	currentRoot, err := s.observations.LoadRoot(ctx, rootID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload probed library root: %w", err)
	}
	if currentRoot.Path != root.Path || currentRoot.MediaType != root.MediaType || currentRoot.MarkerFilename != root.MarkerFilename || currentRoot.Generation != root.Generation {
		return nil, ErrScanInterrupted
	}
	root = currentRoot
	updateHealth := func(health string) error {
		updated, updateErr := s.observations.UpdateProbedHealth(ctx, root, health)
		if errors.Is(updateErr, ports.ErrScanObservationStale) {
			return ErrScanInterrupted
		}
		if updateErr != nil {
			return fmt.Errorf("failed to write probed root health: %w", updateErr)
		}
		root = updated
		return nil
	}
	capability := roots[root.Path]
	marker := root.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	_, probeErr := capability.Inspect(ctx, marker)
	if probeErr != nil {
		probeErr = ErrMarkerMissing
	}
	if probeErr != nil {
		// Mark root offline and abort: never delete files when root is offline
		if err := updateHealth("offline"); err != nil {
			return nil, err
		}
		return nil, probeErr
	}

	identity, err := capability.Identity(ctx)
	if err != nil {
		return nil, ErrRootOffline
	}
	if root.RootIdentity != nil && (identity.Device != root.RootIdentity.Device || identity.Inode != root.RootIdentity.Inode) {
		return nil, ErrScanInterrupted
	}
	// If root was offline, restore health
	if root.Health != "online" {
		if err := updateHealth("online"); err != nil {
			return nil, err
		}
	}

	// A sealed walk resumes under the same physical slot without walking again.
	pending, err := s.observations.FindFinalizing(ctx, rootID)
	if err != nil {
		return nil, err
	}
	if pending != nil {
		return s.finishFinalizing(ctx, capability, *pending)
	}

	root.RootIdentity = &identity
	// 2. Start scan run in database
	runID := s.idgen.NewUUID()
	run, err := s.observations.Start(ctx, root, runID)
	if errors.Is(err, ports.ErrScanObservationStale) {
		return nil, ErrScanInterrupted
	}
	if err != nil {
		return nil, fmt.Errorf("failed to start scan run: %w", err)
	}

	// 3. Secure filesystem walk
	walkErr := s.files.Walk(ctx, capability, func(rel string, info ports.FileIdentity) error {
		name := filepath.Base(rel)
		if name == root.MarkerFilename || strings.HasPrefix(name, ".") {
			return nil
		}

		cleanRel, err := naming.CleanRelPath(rel)
		if err != nil {
			return nil // ignore escaped path
		}

		_, err = s.observations.Record(ctx, ports.ScanObservation{
			RunID: runID, RootID: rootID, ObservationID: s.idgen.NewUUID(), NewFileID: s.idgen.NewUUID(),
			Path: root.Path, MediaType: root.MediaType, MarkerFilename: root.MarkerFilename,
			Generation: run.Generation, HealthRevision: root.HealthRevision, ScanToken: run.ScanToken,
			StartMutationSeq: run.StartMutationSeq, RelPath: cleanRel, Identity: info,
		})
		if errors.Is(err, ports.ErrScanObservationStale) {
			return ErrScanInterrupted
		}
		if err != nil {
			return err
		}
		return nil
	})

	// A replaced/unmounted root cannot produce a complete baseline or missing files.
	if walkErr == nil {
		current, err := s.files.OpenRoot(ctx, root.Path)
		if err != nil {
			walkErr = err
		} else {
			a, firstErr := capability.Identity(ctx)
			b, secondErr := current.Identity(ctx)
			current.Close()
			if firstErr != nil || secondErr != nil || a.Device != b.Device || a.Inode != b.Inode {
				walkErr = ErrScanInterrupted
			}
		}
	}
	if walkErr != nil {
		// Mark run as partial or failed
		seal := ports.ScanSeal{RunID: runID, RootID: rootID, Path: root.Path, MediaType: root.MediaType, MarkerFilename: root.MarkerFilename, Generation: run.Generation, HealthRevision: root.HealthRevision, ScanToken: run.ScanToken, StartMutationSeq: run.StartMutationSeq}
		if err := s.observations.Interrupt(ctx, seal); err != nil {
			return nil, errors.Join(walkErr, err)
		}
		return nil, walkErr
	}

	var capacity *ports.RootCapacity
	if s.capacity != nil {
		observed, sampleErr := s.capacity.SampleCapacity(ctx, capability)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if sampleErr == nil {
			capacity = &observed
		}
	}

	identity, err = capability.Identity(ctx)
	if err != nil {
		return nil, err
	}
	seal := ports.ScanSeal{RunID: runID, RootID: rootID, Path: root.Path, MediaType: root.MediaType, MarkerFilename: root.MarkerFilename,
		Generation: run.Generation, HealthRevision: root.HealthRevision, ScanToken: run.ScanToken, StartMutationSeq: run.StartMutationSeq, RootIdentity: identity, Capacity: capacity}
	if err = s.observations.Seal(ctx, seal); err != nil {
		return nil, err
	}
	return s.finishFinalizing(ctx, capability, seal)
}

func (s *Scanner) finishFinalizing(ctx context.Context, capability ports.TransferRoot, seal ports.ScanSeal) (*ScanReport, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, err := s.files.OpenRoot(ctx, seal.Path)
		if err != nil {
			return nil, s.failFinalizing(ctx, seal, err)
		}
		a, firstErr := capability.Identity(ctx)
		b, secondErr := current.Identity(ctx)
		marker := seal.MarkerFilename
		if marker == "" {
			marker = ".pulsemedia_root"
		}
		_, markerErr := current.Inspect(ctx, marker)
		current.Close()
		if firstErr != nil || secondErr != nil || markerErr != nil || a.Device != seal.RootIdentity.Device || a.Inode != seal.RootIdentity.Inode || b.Device != a.Device || b.Inode != a.Inode {
			return nil, s.failFinalizing(ctx, seal, ErrScanInterrupted)
		}
		progress, err := s.observations.ReconcileBatch(ctx, seal)
		if err != nil {
			if errors.Is(err, ports.ErrScanObservationStale) {
				return nil, s.failFinalizing(ctx, seal, ErrScanInterrupted)
			}
			return nil, err // A DB or cancellation failure keeps the committed cursor resumable.
		}
		if progress.Done {
			return &ScanReport{ScanRunID: progress.RunID, RootID: seal.RootID, FilesObserved: progress.Observed, FilesAdded: progress.Added, FilesUpdated: progress.Updated, MissingCandidates: progress.Missing, MissingReviewRequired: progress.MissingReviewRequired, State: "succeeded"}, nil
		}
	}
}
func (s *Scanner) failFinalizing(ctx context.Context, seal ports.ScanSeal, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	err := s.observations.FailFinalization(ctx, seal)
	if errors.Is(err, ports.ErrScanObservationStale) {
		err = s.observations.AbortFinalization(ctx, seal)
	}
	if err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
