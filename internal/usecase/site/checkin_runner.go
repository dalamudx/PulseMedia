package site

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const (
	checkinRequestTimeout     = 15 * time.Second
	checkinBookkeepingTimeout = 10 * time.Second
	checkinMaxBudgetWait      = 30 * time.Second
)

type CheckinRunner struct {
	repo    ports.SiteCheckinRepository
	builder ports.SiteCheckinClientBuilder
}

func NewCheckinRunner(repo ports.SiteCheckinRepository, builder ports.SiteCheckinClientBuilder) *CheckinRunner {
	return &CheckinRunner{repo: repo, builder: builder}
}

func checkinRunnerError(err error) error {
	if err == nil {
		return nil
	}
	if limit, ok := ports.IsRateLimited(err); ok {
		delay := limit.RetryAfter
		if delay <= 0 {
			delay = 2 * time.Second
		}
		if delay > checkinMaxBudgetWait {
			delay = checkinMaxBudgetWait
		}
		return ports.NewRateLimitError(delay, "site checkin rate limited")
	}
	for _, safe := range []error{ports.ErrAuthRequired, ports.ErrBlocked, ports.ErrBadResponse, ports.ErrRejected, ports.ErrTimeout, ports.ErrUnavailable, ports.ErrEndpointVersionConflict} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	if errors.Is(err, context.Canceled) {
		return errors.Join(ports.ErrUnavailable, context.Canceled)
	}
	return ports.ErrUnavailable
}

func checkinObservationValid(o ports.CheckinObservation, uid string) bool {
	return o.AccountID == uid && (o.Mark == ports.CheckinMarkPending || o.Mark == ports.CheckinMarkAttended) && !o.RequestedAt.IsZero() && !o.ObservedAt.Before(o.RequestedAt)
}

// Execute owns the physical permit until every synchronous native call returns.
func (s *CheckinRunner) Execute(ctx context.Context, lease ports.JobLease) error {
	if ctx.Err() != nil {
		return checkinRunnerError(ctx.Err())
	}
	if lease.ID == uuid.Nil || strings.TrimSpace(lease.Owner) == "" || lease.Token < 1 {
		return ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.builder == nil {
		return ports.ErrUnavailable
	}
	permit, err := s.repo.Acquire(ctx, lease)
	if err != nil {
		return checkinRunnerError(err)
	}
	if permit == nil {
		return ports.ErrUnavailable
	}
	defer permit.Close()
	frozen := permit.Snapshot()
	var outcome ports.SiteCheckinOutcome
	ioReturned := false
	finish := func(failure error) error {
		if ctx.Err() != nil {
			failure = ctx.Err()
		}
		outcome.Failure = checkinRunnerError(failure)
		if failure != nil {
			// Late or invalid positive evidence must never reach persistence.
			outcome.Observation = nil
			if outcome.Submission != nil {
				if outcome.Submission.State == ports.CheckinSubmissionUnknown || outcome.Submission.State == ports.CheckinSubmissionNotSent {
					outcome.Submission.Observation = nil
				} else {
					outcome.Submission = nil
				}
			}
		}
		finishCtx := ctx
		cancel := func() {}
		if ioReturned {
			finishCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), checkinBookkeepingTimeout)
		}
		defer cancel()
		if e := permit.Finish(finishCtx, outcome); e != nil {
			return checkinRunnerError(e)
		}
		return outcome.Failure
	}
	if ctx.Err() != nil {
		return finish(ctx.Err())
	}
	day, parseErr := time.Parse("2006-01-02", frozen.CalendarDay)
	if parseErr != nil || day.Format("2006-01-02") != frozen.CalendarDay || frozen.DayID == uuid.Nil || frozen.ExecutionID == uuid.Nil || frozen.Config.IndexerID == uuid.Nil || !checkinConfigValid(frozen.Config, frozen.Config.IndexerID) || !frozen.Config.Enabled || strings.TrimSpace(frozen.SourceURL) == "" || strings.TrimSpace(frozen.Cookie) == "" {
		return finish(ports.ErrRejected)
	}
	var bounds ports.SiteCheckinWindow
	validateWindow := func(w ports.SiteCheckinWindow) error {
		if w.Now.IsZero() || w.DayStart.IsZero() || w.DayEnd.IsZero() || !w.DayEnd.After(w.DayStart) || w.Now.Before(w.DayStart) || !w.Now.Before(w.DayEnd) {
			return ports.ErrRejected
		}
		if !bounds.DayStart.IsZero() && (!w.DayStart.Equal(bounds.DayStart) || !w.DayEnd.Equal(bounds.DayEnd)) {
			return ports.ErrRejected
		}
		bounds = w
		return nil
	}
	check := func() (ports.SiteCheckinWindow, error) {
		if ctx.Err() != nil {
			return ports.SiteCheckinWindow{}, ctx.Err()
		}
		w, e := permit.Check(ctx)
		if e == nil {
			e = validateWindow(w)
		}
		if ctx.Err() != nil {
			e = ctx.Err()
		}
		return w, e
	}
	if _, err = check(); err != nil {
		return finish(err)
	}
	client, err := s.builder.NewSiteCheckinClient(frozen)
	if err != nil {
		return finish(err)
	}
	if client == nil {
		return finish(ports.ErrUnavailable)
	}
	var waited time.Duration
	waitBudget := func(e error) error {
		limit, ok := ports.IsRateLimited(e)
		if !ok {
			return e
		}
		delay := limit.RetryAfter
		if delay == 0 {
			delay = 2 * time.Second
		}
		if delay < 0 || delay > checkinMaxBudgetWait || waited > checkinMaxBudgetWait-delay {
			return ports.ErrRejected
		}
		w, e := check()
		if e != nil {
			return e
		}
		if !w.Now.Add(delay + checkinRequestTimeout + checkinBookkeepingTimeout).Before(w.DayEnd) {
			return ports.ErrTimeout
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			waited += delay
			return nil
		}
	}
	for {
		if ctx.Err() != nil {
			return finish(ctx.Err())
		}
		w, e := permit.ReserveObserve(ctx)
		if e == nil {
			if e = validateWindow(w); e != nil {
				return finish(e)
			}
			break
		}
		if _, ok := ports.IsRateLimited(e); !ok {
			return finish(e)
		}
		if e = waitBudget(e); e != nil {
			return finish(e)
		}
	}
	if ctx.Err() != nil {
		return finish(ctx.Err())
	}
	requestCtx, cancel := context.WithTimeout(ctx, checkinRequestTimeout)
	observation, readErr := client.ObserveCheckin(requestCtx, frozen.Config.ExpectedAccountID)
	requestErr := requestCtx.Err()
	cancel()
	ioReturned = true
	if ctx.Err() != nil {
		return finish(ctx.Err())
	}
	if requestErr != nil {
		return finish(requestErr)
	}
	if _, err = check(); err != nil {
		return finish(err)
	}
	if readErr != nil {
		return finish(readErr)
	}
	if !checkinObservationValid(observation, frozen.Config.ExpectedAccountID) {
		return finish(ports.ErrBadResponse)
	}
	outcome.Observation = &observation
	if observation.Mark == ports.CheckinMarkAttended {
		return finish(nil)
	}
	if frozen.ObservationOnly {
		return finish(ports.ErrRejected)
	}
	var grant ports.SiteCheckinSendPermission
	for {
		if ctx.Err() != nil {
			return finish(ctx.Err())
		}
		grant, err = permit.PrepareSubmit(ctx, observation)
		if err == nil {
			break
		}
		if _, ok := ports.IsRateLimited(err); !ok {
			return finish(err)
		}
		if err = waitBudget(err); err != nil {
			return finish(err)
		}
	}
	outcome.Permission = &grant
	if grant.ID == uuid.Nil || grant.DayID != frozen.DayID || grant.ExecutionID != frozen.ExecutionID || grant.ValidUntil.IsZero() {
		return finish(ports.ErrBadResponse)
	}
	if err = validateWindow(grant.Window); err != nil {
		return finish(err)
	}
	if !grant.ValidUntil.After(grant.Window.Now) {
		return finish(ports.ErrTimeout)
	}
	w, err := check()
	if err != nil {
		return finish(err)
	}
	margin := checkinRequestTimeout + checkinBookkeepingTimeout
	if !w.Now.Add(margin).Before(w.DayEnd) || !w.Now.Add(margin).Before(grant.ValidUntil) {
		return finish(ports.ErrTimeout)
	}
	if ctx.Err() != nil {
		return finish(ctx.Err())
	}
	requestCtx, cancel = context.WithTimeout(ctx, checkinRequestTimeout)
	submission, sendErr := client.SubmitCheckin(requestCtx, frozen.Config.ExpectedAccountID)
	requestErr = requestCtx.Err()
	cancel()
	outcome.Submission = &submission
	if ctx.Err() != nil {
		return finish(ctx.Err())
	}
	if requestErr != nil {
		return finish(requestErr)
	}
	if _, err = check(); err != nil {
		return finish(err)
	}
	if sendErr != nil {
		return finish(sendErr)
	}
	if submission.State != ports.CheckinSubmissionAttended || submission.Observation == nil || !checkinObservationValid(*submission.Observation, frozen.Config.ExpectedAccountID) || submission.Observation.Mark != ports.CheckinMarkAttended {
		return finish(ports.ErrBadResponse)
	}
	copyObservation := *submission.Observation
	submission.Observation = &copyObservation
	outcome.Submission = &submission
	outcome.Observation = &copyObservation
	return finish(nil)
}
