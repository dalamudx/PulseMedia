package seeding

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type Runner struct {
	repo    ports.SeedingExecutionRepository
	factory ports.SeedingExecutionFactory
}

func NewRunner(repo ports.SeedingExecutionRepository, factory ports.SeedingExecutionFactory) *Runner {
	return &Runner{repo: repo, factory: factory}
}
func executionError(err error) error {
	if err == nil {
		return nil
	}
	if limited, ok := ports.IsRateLimited(err); ok {
		delay := limited.RetryAfter
		if delay <= 0 {
			delay = 10 * time.Second
		}
		if delay > time.Hour {
			delay = time.Hour
		}
		return ports.NewRateLimitError(delay, "seeding request limited")
	}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrForbidden, ports.ErrRejected, ports.ErrNotFound, ports.ErrBadResponse, ports.ErrAuthRequired, ports.ErrBlocked, ports.ErrTimeout, ports.ErrUnavailable, ports.ErrSeedingConfigConflict, ports.ErrSeedingLeaseLost} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}
func sameRemovalTarget(observed *ports.SeedingRemoteTorrent, target ports.SeedingRemovalTarget) bool {
	return observed != nil && observed.InfoHash == target.InfoHash && observed.OperationTag == target.OperationTag && observed.SavePath == target.SavePath && observed.TotalBytes == target.TotalBytes
}
func validExecution(v ports.SeedingExecution) bool {
	stamp, err := hex.DecodeString(v.ClientStamp)
	return err == nil && len(stamp) == 32 && v.ClientStamp == strings.ToLower(v.ClientStamp) && v.TaskID != uuid.Nil && v.ClientID != uuid.Nil && v.Target.InfoHash != "" && v.Target.OperationTag != "" && v.Target.SavePath != "" && v.Target.TotalBytes > 0 && (v.ObserveOnly || v.Rule.Validate() == nil)
}

// Execute joins every native call before releasing the physical gate. Once a
// permanent permit exists, even a known pre-dispatch interruption is observe-only.
func (s *Runner) Execute(ctx context.Context, lease ports.JobLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease.ID == uuid.Nil || strings.TrimSpace(lease.Owner) == "" || lease.Token < 1 {
		return ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.factory == nil {
		return ports.ErrUnavailable
	}
	permit, err := s.repo.Acquire(ctx, lease)
	if err != nil {
		return executionError(err)
	}
	if permit == nil {
		return ports.ErrUnavailable
	}
	defer permit.Close()
	snapshot := permit.Snapshot()
	snapshot.Rule = cloneRule(snapshot.Rule)
	if !validExecution(snapshot) {
		return ports.ErrRejected
	}
	check := func(c context.Context) error {
		if err := c.Err(); err != nil {
			return err
		}
		return executionError(permit.Check(c))
	}
	reserve := func(c context.Context) error {
		if err := check(c); err != nil {
			return err
		}
		if err := permit.Reserve(c); err != nil {
			return executionError(err)
		}
		return check(c)
	}
	finish := func(result ports.SeedingExecutionResult, failure error) error {
		if err := check(ctx); err != nil {
			return err
		}
		failure = executionError(failure)
		if err := permit.Finish(ctx, result, failure); err != nil {
			return executionError(err)
		}
		return failure
	}
	if err = check(ctx); err != nil {
		return err
	}
	if !snapshot.ObserveOnly && (!snapshot.Protection.Owned || !snapshot.Protection.Imported || snapshot.Protection.PendingWork) {
		return finish(ports.SeedingExecutionIneligible, nil)
	}
	native, err := s.factory.OpenSeedingExecution(ctx, snapshot.ClientID, snapshot.ClientStamp)
	if err != nil {
		return executionError(err)
	}
	if native.Reader == nil || !snapshot.ObserveOnly && native.Remover == nil {
		return ports.ErrUnavailable
	}
	if snapshot.ObserveOnly {
		if err = reserve(ctx); err != nil {
			return err
		}
		readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		observed, readErr := native.Reader.ReadSeedingTorrent(readCtx, snapshot.Target.InfoHash, snapshot.Target.OperationTag)
		if readCtx.Err() != nil {
			readErr = readCtx.Err()
		}
		cancel()
		if errors.Is(readErr, ports.ErrNotFound) {
			return finish(ports.SeedingExecutionAbsent, nil)
		}
		if readErr != nil {
			return finish(ports.SeedingExecutionUnknown, readErr)
		}
		if !sameRemovalTarget(observed, snapshot.Target) {
			return finish(ports.SeedingExecutionUnknown, ports.ErrRejected)
		}
		return finish(ports.SeedingExecutionPresent, nil)
	}
	var authorized atomic.Bool
	var prepared atomic.Bool
	ineligible := errors.New("seeding criteria no longer met")
	authorize := func(c context.Context, observed ports.SeedingRemoteTorrent) error {
		if !authorized.CompareAndSwap(false, true) {
			return ports.ErrRejected
		}
		if err := check(c); err != nil {
			return err
		}
		if !sameRemovalTarget(&observed, snapshot.Target) {
			return ports.ErrRejected
		}
		// Space is sampled for this path after the last identity observation. Never
		// trust a capacity field supplied by the torrent endpoint itself.
		observed.Facts.FreeBytes = nil
		observed.Facts.FreeSpaceMatchesDownloadPath = false
		if snapshot.Rule.LowFreeBytes != nil {
			if err := reserve(c); err != nil {
				return err
			}
			space, err := native.Reader.ReadSeedingFreeSpace(c, snapshot.Target.SavePath)
			if err != nil {
				return executionError(err)
			}
			if space.Path == snapshot.Target.SavePath && space.FreeBytes != nil {
				n := *space.FreeBytes
				observed.Facts.FreeBytes = &n
				observed.Facts.FreeSpaceMatchesDownloadPath = true
			}
		}
		decision, err := rule.Evaluate(&observed.Facts, snapshot.Protection, snapshot.Rule)
		if err != nil {
			return ports.ErrBadResponse
		}
		if !decision.Eligible {
			return ineligible
		}
		if err = reserve(c); err != nil {
			return err
		}
		if err = permit.Prepare(c, observed); err != nil {
			return executionError(err)
		}
		prepared.Store(true)
		return check(c)
	}
	remover, err := native.Remover(snapshot.Target, authorize)
	if err != nil {
		return executionError(err)
	}
	if remover == nil {
		return ports.ErrUnavailable
	}
	// The native capability performs one fresh identity read before authorizing.
	if err = reserve(ctx); err != nil {
		return err
	}
	nativeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	outcome, removeErr := remover.RemoveRetainingFiles(nativeCtx)
	if nativeCtx.Err() != nil {
		removeErr = nativeCtx.Err()
	}
	cancel()
	if err = ctx.Err(); err != nil {
		return err
	}
	if prepared.Load() {
		if outcome == ports.SeedingRemovalAcknowledged && removeErr == nil {
			return finish(ports.SeedingExecutionAcknowledged, nil)
		}
		return finish(ports.SeedingExecutionUnknown, removeErr)
	}
	if errors.Is(removeErr, ineligible) {
		return finish(ports.SeedingExecutionIneligible, nil)
	}
	if outcome == ports.SeedingRemovalAbsent && removeErr == nil {
		return finish(ports.SeedingExecutionAbsent, nil)
	}
	if outcome != ports.SeedingRemovalNotDispatched {
		return finish(ports.SeedingExecutionUnknown, ports.ErrBadResponse)
	}
	if removeErr == nil {
		removeErr = ports.ErrBadResponse
	}
	return finish(ports.SeedingExecutionFailed, removeErr)
}
