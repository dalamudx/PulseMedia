package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type movieUpgradeOrganizationWorker struct {
	runtime    ports.MovieUpgradeOrganizationRuntime
	jobs       ports.MovieUpgradeOrganizationJobRepository
	execution  ports.MovieUpgradeOrganizationExecutionRepository
	ingestion  ports.MovieUpgradeOrganizationIngestionRepository
	retirement ports.MovieUpgradeRetirementRuntime
}

func (a *App) movieUpgradeOrganizationServices() (*movieUpgradeOrganizationWorker, error) {
	store, e := postgres.NewUpgradePreparationStore(a.dbPool, a.idgen, torrent.Inspector{})
	if e != nil {
		return nil, e
	}
	runtime, e := postgres.NewMovieUpgradeOrganizationRuntimeStore(store)
	if e != nil {
		return nil, e
	}
	jobs, e := postgres.NewMovieUpgradeOrganizationJobRepository(store)
	if e != nil {
		return nil, e
	}
	files := rootfs.NewFilesystem()
	execution, e := postgres.NewMovieUpgradeOrganizationExecutionRepository(store, files)
	if e != nil {
		return nil, e
	}
	ingestion, e := postgres.NewMovieUpgradeOrganizationIngestionRepository(store, files, outbox.New(a.dbPool, a.idgen, a.clock))
	if e != nil {
		return nil, e
	}
	retirement, e := postgres.NewMovieUpgradeRetirementStore(store, files)
	if e != nil {
		return nil, e
	}
	return &movieUpgradeOrganizationWorker{runtime: runtime, jobs: jobs, execution: execution, ingestion: ingestion, retirement: retirement}, nil
}
func movieUpgradeOrganizationPayload(raw []byte) (uuid.UUID, error) {
	fail := ports.ErrMovieUpgradeOrganizationReview
	d := json.NewDecoder(bytes.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return uuid.Nil, fail
	}
	key, e := d.Token()
	if e != nil || key != "organization_job_id" {
		return uuid.Nil, fail
	}
	var value string
	if d.Decode(&value) != nil {
		return uuid.Nil, fail
	}
	id, e := uuid.Parse(value)
	if e != nil || id == uuid.Nil || id.String() != value {
		return uuid.Nil, fail
	}
	t, e = d.Token()
	if e != nil || t != json.Delim('}') {
		return uuid.Nil, fail
	}
	if _, e = d.Token(); !errors.Is(e, io.EOF) {
		return uuid.Nil, fail
	}
	return id, nil
}
func (a *App) runMovieUpgradeOrganizationJob(ctx context.Context, q movieUpgradeObservationQueue, w *movieUpgradeOrganizationWorker, j *db.Job) {
	id, err := movieUpgradeOrganizationPayload(j.Payload)
	if err == nil && id == j.ID && j.Kind == ports.MovieUpgradeOrganizationJobKind && j.State == "running" && j.Attempts == 1 && j.MaxAttempts == 1 && j.LeaseOwner.Valid && j.LeaseUntil.Valid && j.LeaseToken > 0 {
		l := ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
		var actor *ports.Actor
		actor, err = w.runtime.MovieUpgradeOrganizationClaim(ctx, l)
		if err == nil {
			_, err = w.jobs.CaptureMovieUpgradeOrganizationJob(ctx, actor, l)
		}
		if err == nil {
			_, err = w.execution.ExecuteMovieUpgradeOrganization(ctx, actor, l)
		}
		if err == nil {
			return
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if e := q.Fail(cleanup, j.ID, j.LeaseOwner.String, j.LeaseToken, "upgrade_organization_manual_review", time.Second); e != nil {
		a.logger.Warn("upgrade organization job remains fenced")
	}
}
func (a *App) ingestPendingMovieUpgrades(ctx context.Context, w *movieUpgradeOrganizationWorker) {
	pending, e := w.runtime.PendingMovieUpgradeOrganizationIngestion(ctx)
	if e != nil {
		if ctx.Err() == nil {
			a.logger.Warn("upgrade ingestion scheduling unavailable")
		}
		return
	}
	for _, p := range pending {
		if ctx.Err() != nil {
			return
		}
		if _, e = w.ingestion.IngestMovieUpgradeOrganization(ctx, &p.Actor, p.JobID); e != nil && ctx.Err() == nil {
			a.logger.Warn("upgrade ingestion requires review")
		}
	}
}
func (a *App) runMovieUpgradeOrganizationWorker(ctx context.Context, q movieUpgradeObservationQueue, w *movieUpgradeOrganizationWorker) {
	if q == nil || w == nil {
		return
	}
	owner := a.idgen.NewUUIDString()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	next := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		if !a.clock.Now().Before(next) {
			a.ingestPendingMovieUpgrades(ctx, w)
			next = a.clock.Now().Add(30 * time.Second)
			a.retirePendingMovieUpgrades(ctx, w)
		}
		claim, cancel := context.WithTimeout(ctx, 5*time.Second)
		j, e := q.Claim(claim, []string{ports.MovieUpgradeOrganizationJobKind}, owner, 2*time.Minute)
		cancel()
		if e == nil && j != nil {
			a.runMovieUpgradeOrganizationJob(ctx, q, w, j)
			continue
		}
		if e != nil && ctx.Err() == nil {
			a.logger.Warn("upgrade organization claim unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}

func (a *App) retirePendingMovieUpgrades(ctx context.Context, w *movieUpgradeOrganizationWorker) {
	if w.retirement == nil {
		return
	}
	pending, e := w.retirement.PendingMovieUpgradeRetirements(ctx, 25)
	if e != nil {
		if ctx.Err() == nil {
			a.logger.Warn("upgrade retirement scheduling unavailable")
		}
		return
	}
	for _, p := range pending {
		if ctx.Err() != nil {
			return
		}
		actor := &ports.Actor{ID: p.ActorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
		if e = w.retirement.ExecuteMovieUpgradeRetirement(ctx, actor, p.OperationID); e != nil && ctx.Err() == nil {
			a.logger.Warn("upgrade retirement requires review")
		}
	}
}
