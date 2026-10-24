package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	mediaUsecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"time"
)

func (a *App) runMovieRecognitionConsumer(ctx context.Context, queue *jobs.Queue, runtime *postgres.MovieRecognitionStore, provider ports.MovieIdentityProvider) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		job, err := queue.Claim(ctx, []string{"movie_recognition"}, owner, 5*time.Minute)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("movie recognition claim deferred")
		}
		if job != nil {
			workCtx, cancel := context.WithTimeout(ctx, 110*time.Second)
			work, workErr := runtime.Work(workCtx, ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken})
			if workErr == nil {
				repo := postgres.NewCheckedMovieRecognitionStoreWithMinimumMediaBytes(a.dbPool, a.idgen, work, a.cfg.Organization.MinMediaBytes)
				workErr = mediaUsecase.NewMovieRecognizer(rootfs.NewFilesystem(), repo, provider).Recognize(workCtx, work.RootID, work.RelPath)
			}
			cancel()
			if ctx.Err() != nil {
				return
			}
			if workErr != nil {
				if err = queue.Fail(ctx, job.ID, owner, job.LeaseToken, "movie recognition interrupted; retry required", 30*time.Second); err != nil {
					a.logger.Error("movie recognition failure write rejected")
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
