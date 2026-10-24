package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"sync"
	"time"
)

// App.Run owns scheduling and two independent physical consumers until drained.
func (a *App) runLibraryWorkers(ctx context.Context, queue *jobs.Queue, provider ports.MovieIdentityProvider) {
	runtime := postgres.NewLibraryScanRuntimeStore(a.dbPool, a.idgen)
	scheduler := library.NewScanScheduler(runtime)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); a.runLibraryConsumer(ctx, queue, runtime) }()
	}
	recognition := postgres.NewMovieRecognitionStoreWithMinimumMediaBytes(a.dbPool, a.idgen, a.cfg.Organization.MinMediaBytes)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); a.runMovieRecognitionConsumer(ctx, queue, recognition, provider) }()
	}
	defer group.Wait()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if _, err := db.New(a.dbPool).RecoverExpiredLibraryScanJobs(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("library task recovery deferred")
		}
		if _, err := db.New(a.dbPool).RecoverExpiredMovieRecognitionJobs(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("movie recognition recovery deferred")
		}
		if _, err := recognition.QueueDue(ctx, 20); err != nil && ctx.Err() == nil {
			a.logger.Error("movie recognition scheduling deferred")
		}
		if _, err := scheduler.QueueDue(ctx, 20); err != nil && ctx.Err() == nil {
			a.logger.Error("library scan scheduling deferred")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *App) runLibraryConsumer(ctx context.Context, queue *jobs.Queue, runtime *postgres.LibraryScanRuntimeStore) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		job, err := queue.Claim(ctx, []string{"library_scan", "library_missing"}, owner, 5*time.Minute)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("library task claim deferred")
		}
		if job != nil {
			workCtx, cancel := context.WithTimeout(ctx, 110*time.Second)
			work, workErr := runtime.Work(workCtx, ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken})
			if workErr == nil {
				repo := postgres.NewLeasedScanObservationStore(a.dbPool, a.idgen, work)
				files := rootfs.NewFilesystem()
				switch work.Phase {
				case "scan":
					_, workErr = library.NewScanner(a.idgen, files, repo).WithCapacitySampler(rootfs.NewCapacitySampler(a.clock)).ScanRoot(workCtx, work.RootID)
				case "missing":
					_, workErr = library.NewMissingConfirmer(files, repo).Confirm(workCtx, work.RootID, work.RunID)
					if workErr == nil || errors.Is(workErr, ports.ErrScanObservationStale) {
						workErr = repo.FinishMissingWork(workCtx)
					}
				default:
					workErr = ports.ErrScanObservationStale
				}
			}
			cancel()
			if ctx.Err() != nil {
				return
			}
			if workErr != nil {
				if err = queue.Fail(ctx, job.ID, owner, job.LeaseToken, "library observation interrupted; retry required", 30*time.Second); err != nil {
					a.logger.Error("library task failure write rejected")
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
