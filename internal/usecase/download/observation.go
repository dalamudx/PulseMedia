package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

func ReconcileTask(ctx context.Context, repo ports.DownloadObservationPreparer, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	return observeDownload(ctx, repo, op, true, nil)
}
func ObserveTask(ctx context.Context, repo ports.DownloadObservationPreparer, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	return observeDownload(ctx, repo, op, false, nil)
}
func ObserveDownloadWatchTarget(ctx context.Context, repo ports.DownloadObservationPreparer, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	return observeDownload(ctx, repo, target.OperationID, false, &target)
}
func observeDownload(ctx context.Context, repo ports.DownloadObservationPreparer, op uuid.UUID, reconcile bool, watch *ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	p, err := repo.PrepareDownloadObservation(ctx, op, reconcile, watch)
	if p == nil {
		if err == nil {
			err = ports.ErrUnavailable
		}
		return nil, err
	}
	if p.Release != nil {
		defer p.Release()
	}
	if err != nil {
		return p.Task, err
	}
	if p.Client == nil {
		if p.Task == nil || p.Complete != nil || p.Release != nil {
			return p.Task, ports.ErrUnavailable
		}
		return p.Task, nil
	}
	if p.Task == nil || p.Complete == nil || p.Release == nil {
		return p.Task, ports.ErrUnavailable
	}
	result := ports.DownloadObservationResult{Err: ctx.Err()}
	if result.Err == nil {
		if reconcile && p.Task.InfoHash == "" {
			var matches []*ports.TorrentInfo
			tag := "pulsemedia:op:" + op.String()
			matches, result.Err = p.Client.ListTorrents(ctx, tag)
			count := 0
			for _, candidate := range matches {
				if candidate == nil {
					continue
				}
				for _, label := range strings.Split(candidate.Tags, ",") {
					if strings.TrimSpace(label) == tag {
						count++
						result.Info = candidate
						break
					}
				}
			}
			if count != 1 {
				result.Info = nil
			}
		} else {
			result.Info, result.Err = p.Client.GetTorrent(ctx, p.Task.InfoHash)
		}
	}
	if result.Err == nil && result.Info != nil && p.InventoryRequired != nil {
		required, e := p.InventoryRequired(ctx, result.Info)
		if e != nil {
			result.Err = e
		} else if required {
			provider, ok := p.Client.(ports.DownloadFileInventoryClient)
			if !ok {
				result.Err = ports.ErrDownloadReconcilePending
			} else {
				result.Files, result.Err = provider.GetTorrentFiles(ctx, p.Task.InfoHash)
				if result.Err == nil {
					result.Second, result.Err = p.Client.GetTorrent(ctx, p.Task.InfoHash)
				}
			}
		}
	}
	return p.Complete(ctx, result)
}
