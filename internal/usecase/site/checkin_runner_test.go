package site_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
)

type checkinFake struct {
	work                                            ports.SiteCheckinExecution
	window                                          ports.SiteCheckinWindow
	grant                                           ports.SiteCheckinSendPermission
	outcome                                         ports.SiteCheckinOutcome
	gets, posts, reserves, prepares, builds, checks int
	closed                                          atomic.Bool
	prepareErr, checkErr, finishErr, buildErr       error
	prepareRate, reserveRate                        bool
	checkHook                                       func(*checkinFake)
	observe                                         func(context.Context) (ports.CheckinObservation, error)
	submit                                          func(context.Context) (ports.CheckinSubmission, error)
	built                                           ports.SiteCheckinExecution
	cleanupContextOK                                bool
}

func (f *checkinFake) EnqueueDue(context.Context) error     { return nil }
func (f *checkinFake) RecoverExpired(context.Context) error { return nil }
func (f *checkinFake) Acquire(context.Context, ports.JobLease) (ports.SiteCheckinPermit, error) {
	return f, nil
}
func (f *checkinFake) Snapshot() ports.SiteCheckinExecution { return f.work }
func (f *checkinFake) Check(context.Context) (ports.SiteCheckinWindow, error) {
	f.checks++
	if f.checkHook != nil {
		f.checkHook(f)
	}
	return f.window, f.checkErr
}
func (f *checkinFake) ReserveObserve(context.Context) (ports.SiteCheckinWindow, error) {
	f.reserves++
	if f.reserveRate && f.reserves == 1 {
		return ports.SiteCheckinWindow{}, ports.NewRateLimitError(20*time.Millisecond, "secret")
	}
	return f.window, nil
}
func (f *checkinFake) PrepareSubmit(_ context.Context, o ports.CheckinObservation) (ports.SiteCheckinSendPermission, error) {
	f.prepares++
	if o.AccountID != f.work.Config.ExpectedAccountID || o.Mark != ports.CheckinMarkPending {
		return ports.SiteCheckinSendPermission{}, ports.ErrBadResponse
	}
	if f.prepareRate && f.prepares == 1 {
		return ports.SiteCheckinSendPermission{}, ports.NewRateLimitError(20*time.Millisecond, "secret")
	}
	return f.grant, f.prepareErr
}
func (f *checkinFake) Finish(ctx context.Context, o ports.SiteCheckinOutcome) error {
	f.outcome = o
	_, deadline := ctx.Deadline()
	f.cleanupContextOK = ctx.Err() == nil && deadline
	return f.finishErr
}
func (f *checkinFake) Close() { f.closed.Store(true) }
func (f *checkinFake) NewSiteCheckinClient(w ports.SiteCheckinExecution) (ports.SiteCheckinNativeClient, error) {
	f.builds++
	f.built = w
	return f, f.buildErr
}
func (f *checkinFake) ObserveCheckin(ctx context.Context, uid string) (ports.CheckinObservation, error) {
	f.gets++
	if uid != f.work.Config.ExpectedAccountID {
		return ports.CheckinObservation{}, ports.ErrRejected
	}
	if f.observe != nil {
		return f.observe(ctx)
	}
	return f.observation(ports.CheckinMarkPending), nil
}
func (f *checkinFake) SubmitCheckin(ctx context.Context, uid string) (ports.CheckinSubmission, error) {
	f.posts++
	if uid != f.work.Config.ExpectedAccountID {
		return ports.CheckinSubmission{}, ports.ErrRejected
	}
	if f.submit != nil {
		return f.submit(ctx)
	}
	o := f.observation(ports.CheckinMarkAttended)
	return ports.CheckinSubmission{State: ports.CheckinSubmissionAttended, Observation: &o}, nil
}
func (f *checkinFake) observation(mark ports.CheckinMark) ports.CheckinObservation {
	// Deliberately decades away from the PostgreSQL window.
	at := time.Date(1999, 1, 1, 1, 0, 0, 0, time.UTC)
	return ports.CheckinObservation{AccountID: f.work.Config.ExpectedAccountID, Mark: mark, RequestedAt: at, ObservedAt: at.Add(time.Second)}
}
func checkinFixture() *checkinFake {
	start := time.Date(2030, 1, 1, 16, 0, 0, 0, time.UTC)
	f := &checkinFake{window: ports.SiteCheckinWindow{Now: start.Add(time.Hour), DayStart: start, DayEnd: start.Add(24 * time.Hour)}}
	f.work = ports.SiteCheckinExecution{DayID: uuid.New(), ExecutionID: uuid.New(), CalendarDay: "2030-01-02", SourceURL: "https://private.invalid/secret", Cookie: "private-cookie", Config: ports.SiteCheckinConfig{IndexerID: uuid.New(), ExpectedAccountID: "42", Profile: ports.NexusPHP1103CheckinProfile, Timezone: "Asia/Shanghai", DailyAt: "02:00", Enabled: true, Version: 1, SourceConfigVersion: 1, CreatedAt: start, UpdatedAt: start}}
	f.grant = ports.SiteCheckinSendPermission{ID: uuid.New(), DayID: f.work.DayID, ExecutionID: f.work.ExecutionID, Window: f.window, ValidUntil: f.window.Now.Add(time.Minute)}
	return f
}
func checkinLease() ports.JobLease { return ports.JobLease{ID: uuid.New(), Owner: "worker", Token: 1} }
func (f *checkinFake) run(ctx context.Context) error {
	return site.NewCheckinRunner(f, f).Execute(ctx, checkinLease())
}

func TestCheckinRunnerPaths(t *testing.T) {
	for _, tc := range []struct {
		name                                            string
		attended, observeOnly, prepareRate, reserveRate bool
		gets, prepares, posts                           int
		fail                                            bool
	}{
		{name: "already_attended", attended: true, gets: 1},
		{name: "prepare_budget", prepareRate: true, gets: 1, prepares: 2, posts: 1},
		{name: "observe_budget", reserveRate: true, gets: 1, prepares: 1, posts: 1},
		{name: "R3_positive", attended: true, observeOnly: true, gets: 1},
		{name: "R3_pending", observeOnly: true, gets: 1, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := checkinFixture()
			f.work.ObservationOnly = tc.observeOnly
			f.prepareRate = tc.prepareRate
			f.reserveRate = tc.reserveRate
			if tc.attended {
				f.observe = func(context.Context) (ports.CheckinObservation, error) {
					return f.observation(ports.CheckinMarkAttended), nil
				}
			}
			err := f.run(context.Background())
			if (err != nil) != tc.fail || f.gets != tc.gets || f.prepares != tc.prepares || f.posts != tc.posts || !f.closed.Load() {
				t.Fatalf("err=%v GET=%d prepare=%d POST=%d closed=%v", err, f.gets, f.prepares, f.posts, f.closed.Load())
			}
			expectedReserve := 1
			if tc.reserveRate {
				expectedReserve = 2
			}
			if f.reserves != expectedReserve {
				t.Fatalf("reserve=%d", f.reserves)
			}
			if !tc.fail && f.outcome.Observation == nil {
				t.Fatal("missing native evidence")
			}
			if tc.posts == 1 && (f.outcome.Permission == nil || f.outcome.Submission == nil) {
				t.Fatal("missing durable grant/submission")
			}
		})
	}
}

func TestCheckinRunnerRejectsBeforePost(t *testing.T) {
	cases := []struct {
		name   string
		change func(*checkinFake)
	}{
		{"prepare_error", func(f *checkinFake) { f.prepareErr = errors.New("commit unknown private-cookie") }},
		{"grant_id", func(f *checkinFake) { f.grant.ID = uuid.Nil }},
		{"grant_identity", func(f *checkinFake) { f.grant.ExecutionID = uuid.New() }},
		{"grant_window", func(f *checkinFake) { f.grant.Window.DayStart = f.grant.Window.DayStart.Add(time.Second) }},
		{"grant_expired", func(f *checkinFake) { f.grant.ValidUntil = f.window.Now }},
		{"midnight_margin", func(f *checkinFake) {
			f.window.Now = f.window.DayEnd.Add(-24 * time.Second)
			f.grant.Window = f.window
			f.grant.ValidUntil = f.window.DayEnd
		}},
		{"wrong_uid", func(f *checkinFake) {
			f.observe = func(context.Context) (ports.CheckinObservation, error) {
				o := f.observation(ports.CheckinMarkAttended)
				o.AccountID = "43"
				return o, nil
			}
		}},
		{"wrong_marker", func(f *checkinFake) {
			f.observe = func(context.Context) (ports.CheckinObservation, error) { return f.observation("success"), nil }
		}},
		{"bad_native_time", func(f *checkinFake) {
			f.observe = func(context.Context) (ports.CheckinObservation, error) {
				o := f.observation(ports.CheckinMarkPending)
				o.ObservedAt = o.RequestedAt.Add(-time.Second)
				return o, nil
			}
		}},
		{"lost_lease", func(f *checkinFake) {
			f.checkHook = func(f *checkinFake) {
				if f.checks == 2 {
					f.checkErr = ports.ErrStaleDeliveryLease
				}
			}
			f.finishErr = ports.ErrStaleDeliveryLease
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := checkinFixture()
			tc.change(f)
			err := f.run(context.Background())
			if err == nil || f.posts != 0 || f.outcome.Observation != nil || !f.closed.Load() {
				t.Fatalf("err=%v posts=%d outcome=%+v", err, f.posts, f.outcome)
			}
			if strings.Contains(err.Error(), "private-cookie") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestCheckinRunnerNoResend(t *testing.T) {
	for _, state := range []ports.CheckinSubmissionState{ports.CheckinSubmissionUnknown, ports.CheckinSubmissionNotSent, "invalid", ports.CheckinSubmissionAttended} {
		t.Run(string(state), func(t *testing.T) {
			f := checkinFixture()
			f.submit = func(context.Context) (ports.CheckinSubmission, error) {
				o := f.observation(ports.CheckinMarkPending)
				return ports.CheckinSubmission{State: state, Observation: &o}, nil
			}
			if err := f.run(context.Background()); err == nil || f.posts != 1 || f.outcome.Failure == nil || f.outcome.Permission == nil || f.outcome.Observation != nil {
				t.Fatalf("err=%v posts=%d outcome=%+v", err, f.posts, f.outcome)
			}
		})
	}
}

func TestCheckinRunnerRetainsGateUntilNativeReturns(t *testing.T) {
	for _, post := range []bool{false, true} {
		name := "observe"
		if post {
			name = "post"
		}
		t.Run(name, func(t *testing.T) {
			f := checkinFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			block := func() { close(entered); <-release }
			if post {
				f.submit = func(context.Context) (ports.CheckinSubmission, error) {
					block()
					o := f.observation(ports.CheckinMarkAttended)
					return ports.CheckinSubmission{State: ports.CheckinSubmissionAttended, Observation: &o}, nil
				}
			} else {
				f.observe = func(context.Context) (ports.CheckinObservation, error) {
					block()
					return f.observation(ports.CheckinMarkAttended), nil
				}
			}
			go func() { done <- f.run(ctx) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("native call not entered")
			}
			cancel()
			if f.closed.Load() {
				close(release)
				<-done
				t.Fatal("physical gate released during native I/O")
			}
			select {
			case err := <-done:
				close(release)
				t.Fatalf("runner returned before I/O: %v", err)
			default:
			}
			close(release)
			select {
			case err := <-done:
				if err == nil || !errors.Is(f.outcome.Failure, context.Canceled) || f.outcome.Observation != nil || f.outcome.Submission != nil || !f.closed.Load() || !f.cleanupContextOK {
					t.Fatalf("err=%v outcome=%+v cleanup=%v", err, f.outcome, f.cleanupContextOK)
				}
			case <-time.After(time.Second):
				t.Fatal("runner did not terminate")
			}
		})
	}
}

func TestCheckinRunnerPrivateBoundary(t *testing.T) {
	f := checkinFixture()
	f.buildErr = errors.New("https://private.invalid/secret private-cookie")
	err := f.run(context.Background())
	if err == nil || strings.Contains(err.Error(), "private") || f.gets != 0 || f.built != f.work {
		t.Fatalf("builder boundary: %v", err)
	}
	data, e := json.Marshal(f.built)
	if e != nil || string(data) != "{}" {
		t.Fatalf("private snapshot serialized: %s %v", data, e)
	}
	for _, change := range []func(*checkinFake){func(f *checkinFake) { f.work.Config.ExpectedAccountID = "042" }, func(f *checkinFake) { f.work.CalendarDay = "2030-02-30" }, func(f *checkinFake) { f.work.Cookie = " " }, func(f *checkinFake) { f.work.Config.Enabled = false }, func(f *checkinFake) { f.window.Now = f.window.DayEnd }} {
		f := checkinFixture()
		change(f)
		if f.run(context.Background()) == nil || f.builds != 0 {
			t.Fatal("invalid snapshot/window reached builder")
		}
	}
}

func TestCheckinRunnerBudgetBounds(t *testing.T) {
	for _, delay := range []time.Duration{-time.Second, time.Hour} {
		t.Run(delay.String(), func(t *testing.T) {
			f := checkinFixture()
			f.prepareErr = ports.NewRateLimitError(delay, "private-cookie")
			err := f.run(context.Background())
			if err == nil || f.posts != 0 || f.prepares != 1 {
				t.Fatalf("unreasonable budget: %v", err)
			}
		})
	}
	f := checkinFixture()
	f.prepareErr = ports.NewRateLimitError(0, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.run(ctx); err == nil || f.gets != 1 || f.posts != 0 || f.prepares != 1 {
		t.Fatalf("budget deadline: %v", err)
	}
}

func TestCheckinRunnerGrantAndLateFences(t *testing.T) {
	for _, mode := range []string{"cancel_after_grant", "lease_lost_after_post", "window_changed_after_post", "unknown_transport_error"} {
		t.Run(mode, func(t *testing.T) {
			f := checkinFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.checkHook = func(f *checkinFake) {
				if mode == "cancel_after_grant" && f.checks == 3 {
					cancel()
				}
				if mode == "lease_lost_after_post" && f.checks == 4 {
					f.checkErr = ports.ErrStaleDeliveryLease
					f.finishErr = ports.ErrStaleDeliveryLease
				}
				if mode == "window_changed_after_post" && f.checks == 4 {
					f.window.DayEnd = f.window.DayEnd.Add(time.Hour)
				}
			}
			if mode == "unknown_transport_error" {
				f.submit = func(context.Context) (ports.CheckinSubmission, error) {
					return ports.CheckinSubmission{State: ports.CheckinSubmissionUnknown}, errors.New("private-cookie")
				}
			}
			err := f.run(ctx)
			expected := 1
			if mode == "cancel_after_grant" {
				expected = 0
			}
			if err == nil || f.posts != expected || f.outcome.Permission == nil || f.outcome.Observation != nil || !f.closed.Load() {
				t.Fatalf("err=%v posts=%d outcome=%+v", err, f.posts, f.outcome)
			}
			if mode == "unknown_transport_error" && (f.outcome.Submission == nil || f.outcome.Submission.State != ports.CheckinSubmissionUnknown) {
				t.Fatal("lost unknown native result")
			}
		})
	}
}
