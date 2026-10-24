package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedDiscoveryWatch struct {
	scheduler ports.UntrackedDiscoveryScheduler
}

func (w *untrackedDiscoveryWatch) ListDownloadWatchSources(ctx context.Context, q ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
	var out ports.DownloadWatchSourcePage
	if w == nil || w.scheduler == nil {
		return out, ports.ErrUnavailable
	}
	page, err := w.scheduler.ListUntrackedDiscoveryScopes(ctx, q.Cursor, q.Limit)
	if err != nil {
		return out, err
	}
	if page.ManifestAt.IsZero() {
		return out, ports.ErrUnavailable
	}
	out.NextCursor, out.Complete = page.NextCursor, page.Complete
	for _, scope := range page.Scopes {
		binding := untrackedDiscoveryWatchBinding(scope)
		if !scope.Enabled || !validDownloadWatchBinding(binding) {
			out.Issues = append(out.Issues, ports.DownloadWatchCoverageIssue{ClientID: scope.ClientID, RootID: scope.TargetRootID, Reason: "discovery_scope_invalid"})
			continue
		}
		out.Sources = append(out.Sources, ports.DownloadWatchSource{Binding: binding, ManifestAt: page.ManifestAt})
	}
	return out, nil
}
func (w *untrackedDiscoveryWatch) ResolveDownloadWatchTargets(ctx context.Context, b ports.DownloadWatchBinding, h ports.RootChangeHint, q ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
	if err := ctx.Err(); err != nil {
		return ports.DownloadWatchTargetPage{}, err
	}
	if w == nil || w.scheduler == nil {
		return ports.DownloadWatchTargetPage{}, ports.ErrUnavailable
	}
	if b.ScopeID == uuid.Nil || !validDownloadWatchBinding(b) || !validDownloadWatchHint(h) || q.Cursor != "" || q.Limit < 1 {
		return ports.DownloadWatchTargetPage{}, ports.ErrRejected
	}
	// A directory hint requests a fresh scope sweep, not a download observation.
	target := ports.DownloadWatchTarget{Binding: b, ScopeID: b.ScopeID, ScopeVersion: b.ScopeVersion, ClientID: b.ClientID, ClientVersion: b.ClientVersion, ClientStamp: b.ClientStamp, Kind: ports.UntrackedDiscoveryJobKind}
	return ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{target}, Complete: true}, nil
}
func untrackedDiscoveryWatchBinding(scope ports.UntrackedScope) ports.DownloadWatchBinding {
	scope.ClientVersion = scope.ClientVersion.UTC()
	scope.CreatedAt = scope.CreatedAt.UTC()
	scope.UpdatedAt = scope.UpdatedAt.UTC()
	raw, _ := json.Marshal(scope)
	hash := sha256.Sum256(append([]byte("untracked_scope_watch_v1\x00"), raw...))
	return ports.DownloadWatchBinding{Stamp: hex.EncodeToString(hash[:]), ScopeID: scope.ID, ScopeVersion: scope.Version, SourceIdentity: scope.SourceIdentity, ClientID: scope.ClientID, ClientVersion: scope.ClientVersion, ClientStamp: scope.ClientStamp, SourceKind: "path_mapping", RemotePrefix: scope.RemotePrefix, LocalRoot: scope.LocalRoot, RootID: scope.TargetRootID, Generation: scope.TargetRootGeneration, RootPath: scope.TargetRootPath, MediaType: scope.TargetMediaType, MarkerFilename: scope.TargetMarker, BaselineScanID: scope.TargetBaselineScanID, RootIdentity: scope.TargetIdentity}
}

type untrackedDiscoveryHintObserver struct {
	scheduler ports.UntrackedDiscoveryScheduler
	wake      chan<- struct{}
}

func (o *untrackedDiscoveryHintObserver) ObserveDownloadWatchTarget(ctx context.Context, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if o == nil || o.scheduler == nil {
		return nil, ports.ErrUnavailable
	}
	if target.Kind != ports.UntrackedDiscoveryJobKind || downloadWatchTargetIdentity(target) == uuid.Nil || !validDownloadWatchBinding(target.Binding) {
		return nil, ports.ErrRejected
	}
	result, err := o.scheduler.RequestUntrackedDiscovery(ctx, target.ScopeID, target.ScopeVersion)
	if errors.Is(err, ports.ErrUntrackedDiscoveryStale) {
		return nil, ports.ErrDownloadWatchStale
	}
	if err != nil {
		return nil, err
	}
	if result == nil || (result.Queued && result.JobID == uuid.Nil) {
		return nil, ports.ErrUnavailable
	}
	if result.JobID != uuid.Nil {
		select {
		case o.wake <- struct{}{}:
		default:
		}
	}
	return nil, nil
}

type untrackedDiscoveryExecutor interface {
	Execute(context.Context, ports.JobLease) error
}

func (a *App) runUntrackedDiscoveryWorker(ctx context.Context, queue *jobs.Queue, scheduler ports.UntrackedDiscoveryScheduler, svc untrackedDiscoveryExecutor, wake <-chan struct{}) {
	owner := a.idgen.NewUUID().String()
	maintenance := time.NewTimer(0)
	defer maintenance.Stop()
	idle := time.NewTicker(200 * time.Millisecond)
	defer idle.Stop()
	for ctx.Err() == nil {
		select {
		case <-maintenance.C:
			if _, err := scheduler.QueueDueUntrackedDiscoveries(ctx, 64); err != nil && ctx.Err() == nil {
				a.logger.Error("directory discovery scheduling unavailable")
			}
			maintenance.Reset(3 * time.Second)
		default:
		}
		if ctx.Err() != nil {
			return
		}
		job, err := queue.Claim(ctx, []string{ports.UntrackedDiscoveryJobKind}, owner, 2*time.Minute)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("directory discovery claim unavailable")
		}
		if job != nil {
			lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
			err = runOwnedPhysical(ctx, 10*time.Minute, func(ctx context.Context) error {
				return queue.RenewLease(ctx, job.ID, owner, job.LeaseToken, 2*time.Minute)
			}, func(ctx context.Context) error { return svc.Execute(ctx, lease) }, 15*time.Second)
			if err != nil {
				finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if e := queue.Fail(finishCtx, job.ID, owner, job.LeaseToken, "directory discovery interrupted", 5*time.Second); e != nil {
					a.logger.Error("directory discovery failure write rejected")
				}
				cancel()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-idle.C:
		}
	}
}
