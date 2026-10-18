package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"io"
	"time"
)

func movieUpgradeTrackingPayload(raw []byte) (uuid.UUID, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	open, e := d.Token()
	if e != nil || open != json.Delim('{') {
		return uuid.Nil, ports.ErrMovieUpgradeTrackingReview
	}
	key, e := d.Token()
	if e != nil || key != "tracking_job_id" {
		return uuid.Nil, ports.ErrMovieUpgradeTrackingReview
	}
	var value string
	if d.Decode(&value) != nil {
		return uuid.Nil, ports.ErrMovieUpgradeTrackingReview
	}
	id, e := uuid.Parse(value)
	if e != nil || id == uuid.Nil || id.String() != value {
		return uuid.Nil, ports.ErrMovieUpgradeTrackingReview
	}
	close, e := d.Token()
	if e != nil || close != json.Delim('}') {
		return uuid.Nil, ports.ErrMovieUpgradeTrackingReview
	}
	if _, e = d.Token(); !errors.Is(e, io.EOF) {
		return uuid.Nil, ports.ErrMovieUpgradeTrackingReview
	}
	return id, nil
}

type movieUpgradeTrackingQueue interface {
	Claim(context.Context, []string, string, time.Duration) (*db.Job, error)
	Fail(context.Context, uuid.UUID, string, int64, string, time.Duration) error
}

func (a *App) runMovieUpgradeTrackingWorker(ctx context.Context, q movieUpgradeTrackingQueue, runtime ports.MovieUpgradeTrackingRuntime, repo ports.MovieUpgradeNativeJobTrackingRepository) {
	if q == nil || runtime == nil || repo == nil {
		return
	}
	owner := a.idgen.NewUUID().String()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	next := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		if !a.clock.Now().Before(next) {
			if _, e := runtime.ScheduleMovieUpgradeTracking(ctx); e != nil && ctx.Err() == nil {
				a.logger.Warn("upgrade tracking scheduling unavailable")
			}
			next = a.clock.Now().Add(30 * time.Second)
		}
		job, e := q.Claim(ctx, []string{ports.MovieUpgradeTrackingJobKind}, owner, 2*time.Minute)
		if e == nil && job == nil {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				continue
			}
		}
		if e != nil {
			if ctx.Err() == nil {
				a.logger.Warn("upgrade tracking claim unavailable")
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				continue
			}
		}
		fail := func(reason string) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = q.Fail(cleanup, job.ID, owner, job.LeaseToken, reason, time.Second)
		}
		trackingID, payloadErr := movieUpgradeTrackingPayload(job.Payload)
		if payloadErr != nil || trackingID != job.ID {
			fail("upgrade_tracking_invalid_payload")
			continue
		}
		lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
		actor, req, e := runtime.MovieUpgradeTrackingClaim(ctx, lease)
		if e == nil {
			_, e = repo.TrackMovieUpgradeJob(ctx, actor, req)
		}
		if e != nil {
			fail("upgrade_tracking_manual_review")
		}
		// The repository commits result and job success together. No renewal,
		// separate Succeed call, takeover or additional downloader Add is permitted.
	}
}
