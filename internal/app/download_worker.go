package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
)

// downloadSubmission is persisted by the acquisition UoW; it never contains a URL or credentials.
type downloadSubmission struct {
	OperationID uuid.UUID  `json:"operation_id"`
	CandidateID uuid.UUID  `json:"candidate_id"`
	RootID      uuid.UUID  `json:"root_id"`
	ClientID    *uuid.UUID `json:"client_id"`
}

// runDownloadWorker owns its external I/O until it returns. A request handler must
// never submit a torrent on a detached goroutine.
func (a *App) runDownloadWorker(ctx context.Context, queue *jobs.Queue, mgr *download.Manager) {
	owner := a.idgen.NewUUID().String()
	q := db.New(a.dbPool)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var lastReconcile time.Time
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		// Re-claim only after the database has fenced the previous lease; the
		// persisted download_task below prevents repeating an uncertain Add.
		if _, err := q.RecoverExpiredDownloadJobs(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("download job recovery failed", slog.String("error", err.Error()))
		}
		clients, err := q.ListDownloadClients(ctx)
		if err == nil && len(clients) > 0 {
			job, err := queue.Claim(ctx, []string{"download_submission", "episode_download_submission"}, owner, 5*time.Minute)
			if err != nil {
				a.logger.Error("download job claim failed", slog.String("error", err.Error()))
			} else if job != nil {
				// Every download request uses a bounded context; shutdown waits for
				// physical I/O to return before closing the database.
				workCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				result, err := handleDownloadJob(workCtx, q, mgr, job, clients[0].ID)
				cancel()
				if ctx.Err() != nil {
					return
				}
				current, readErr := q.GetJobByID(ctx, job.ID)
				if readErr == nil && current.State != "running" {
					continue
				}
				if err != nil {
					// Never store a raw adapter error: it may include a passkey or link.
					if failErr := queue.Fail(ctx, job.ID, owner, job.LeaseToken, "download submission needs attention", 30*time.Second); failErr != nil {
						a.logger.Error("download job failure write rejected", slog.String("error", failErr.Error()))
					}
				} else if err := queue.Finish(ctx, job.ID, owner, job.LeaseToken, result); err != nil {
					a.logger.Error("download job finish rejected", slog.String("error", err.Error()))
				}
			}
		}
		if ctx.Err() == nil && time.Since(lastReconcile) >= 30*time.Second {
			lastReconcile = time.Now()
			observeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if pending, err := q.ListPendingDownloadTasks(observeCtx); err == nil {
				for _, task := range pending {
					if observeCtx.Err() != nil {
						break
					}
					_, _ = mgr.ReconcileTask(observeCtx, task.OperationID)
				}
			}
			cancel()
			progressCtx, cancelProgress := context.WithTimeout(ctx, 20*time.Second)
			if tracking, err := q.ListTrackingDownloadTasks(progressCtx); err == nil {
				for _, task := range tracking {
					if progressCtx.Err() != nil {
						break
					}
					_, _ = mgr.ObserveTask(progressCtx, task.OperationID)
				}
			}
			cancelProgress()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func handleDownloadJob(ctx context.Context, q *db.Queries, mgr *download.Manager, job *db.Job, defaultClient uuid.UUID) (any, error) {
	var payload downloadSubmission
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, err
	}
	if payload.OperationID == uuid.Nil || payload.RootID == uuid.Nil || payload.CandidateID == uuid.Nil {
		return nil, errors.New("incomplete download job")
	}
	// Existing submitting/unknown means Add may already have happened. Never call
	// SubmitDownload again to infer the outcome: observe the persisted task first.
	existing, err := q.GetDownloadTaskByOpID(ctx, payload.OperationID)
	if err == nil {
		if existing.State == "submitting" || existing.State == "unknown" {
			_, _ = mgr.ReconcileTask(ctx, payload.OperationID)
		}
		return map[string]any{"task_id": existing.ID, "reused": true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	root, err := q.GetLibraryRootByID(ctx, payload.RootID)
	if err != nil {
		return nil, err
	}
	if root.Health != "online" {
		return nil, errors.New("root unavailable")
	}
	clientID := defaultClient
	if payload.ClientID != nil {
		clientID = *payload.ClientID
		if _, err := q.GetDownloadClientByID(ctx, clientID); err != nil {
			return nil, err
		}
	}
	// The task row is inserted before Add; a response loss stays unknown and
	// preserves its reservation for out-of-band reconciliation, never a replay.
	task, err := mgr.SubmitDownload(ctx, payload.OperationID, clientID, payload.CandidateID, root.Path, ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken})
	if task != nil {
		return map[string]any{"task_id": task.ID, "state": task.State}, nil
	}
	return nil, err
}
