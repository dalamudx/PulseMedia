package library

import (
	"context"
	"path"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const untrackedDiscoveryBatch = 128

type UntrackedDiscoveryService struct {
	repo  ports.UntrackedDiscoveryRepository
	files ports.ScanFilesystem
}

func NewUntrackedDiscoveryService(repo ports.UntrackedDiscoveryRepository, files ports.ScanFilesystem) *UntrackedDiscoveryService {
	return &UntrackedDiscoveryService{repo: repo, files: files}
}

// Execute observes files only. A successful job never authorizes publication.
func (s *UntrackedDiscoveryService) Execute(ctx context.Context, lease ports.JobLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.repo == nil || s.files == nil {
		return ports.ErrUnavailable
	}
	capture, err := s.repo.LoadUntrackedDiscovery(ctx, lease)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if capture == nil {
		return ports.ErrUntrackedDiscoveryStale
	}
	snapshot := capture.UntrackedDiscoverySnapshot()
	scope := snapshot.Scope
	if snapshot.Lease != lease || lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 || snapshot.Sequence < 1 || !scope.Enabled || !untrackedScopeValid(scope, scope.ID) {
		return ports.ErrUntrackedDiscoveryStale
	}
	roots, release, err := s.files.LockRoots(ctx, scope.LocalRoot, scope.TargetRootPath)
	if release != nil {
		defer release()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || release == nil || roots[scope.LocalRoot] == nil || roots[scope.TargetRootPath] == nil {
		return ports.ErrUnavailable
	}
	probe := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.repo.ValidateUntrackedDiscovery(ctx, capture); err != nil {
			return err
		}
		evidence, err := probeUntrackedScope(ctx, s.files, ports.UntrackedScopeMaterial{
			LocalRoot:      scope.LocalRoot,
			TargetRoot:     ports.ScanRootSnapshot{ID: scope.TargetRootID, Path: scope.TargetRootPath, MediaType: scope.TargetMediaType, MarkerFilename: scope.TargetMarker, Generation: scope.TargetRootGeneration},
			TargetIdentity: scope.TargetIdentity,
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if evidence.SourceIdentity != untrackedScopePhysical(scope.SourceIdentity) || evidence.TargetIdentity != untrackedScopePhysical(scope.TargetIdentity) {
			return ports.ErrUntrackedDiscoveryStale
		}
		for rootPath, expected := range map[string]ports.FileIdentity{scope.LocalRoot: scope.SourceIdentity, scope.TargetRootPath: scope.TargetIdentity} {
			identity, err := roots[rootPath].Identity(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil || untrackedScopePhysical(identity) != untrackedScopePhysical(expected) {
				return ports.ErrUntrackedDiscoveryStale
			}
		}
		return nil
	}
	if err := probe(); err != nil {
		return err
	}
	batch := make([]ports.UntrackedFileObservation, 0, untrackedDiscoveryBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := probe(); err != nil {
			return err
		}
		for _, observation := range batch {
			current, err := roots[scope.LocalRoot].Inspect(ctx, observation.RelativePath)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil || current != observation.Identity {
				return ports.ErrUntrackedDiscoveryStale
			}
		}
		if err := probe(); err != nil {
			return err
		}
		if err := s.repo.RecordUntrackedDiscovery(ctx, capture, batch); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		batch = batch[:0]
		return nil
	}
	err = s.files.Walk(ctx, roots[scope.LocalRoot], func(relative string, identity ports.FileIdentity) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !untrackedDiscoveryRelative(relative) || !untrackedScopeIdentity(identity) || identity.Size < 0 {
			return ports.ErrRejected
		}
		if !naming.IsVideoPath(relative) {
			return nil
		}
		batch = append(batch, ports.UntrackedFileObservation{RelativePath: relative, Identity: identity})
		if len(batch) == untrackedDiscoveryBatch {
			return flush()
		}
		return nil
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if err = flush(); err != nil {
		return err
	}
	if err = probe(); err != nil {
		return err
	}
	err = s.repo.CompleteUntrackedDiscovery(ctx, capture)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func untrackedDiscoveryRelative(relative string) bool {
	return relative != "" && !path.IsAbs(relative) && path.Clean(relative) == relative && relative != ".." && !strings.HasPrefix(relative, "../") && !strings.ContainsAny(relative, "\x00\\")
}
