package site_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
)

type statsRuntimeRepo struct {
	permit          *statsRuntimePermit
	view            *ports.SiteAccountStatsView
	err             error
	acquired, reads int
}

func (r *statsRuntimeRepo) EnqueueDue(context.Context) error     { return nil }
func (r *statsRuntimeRepo) RecoverExpired(context.Context) error { return nil }
func (r *statsRuntimeRepo) Acquire(context.Context, ports.JobLease) (ports.SiteAccountStatsPermit, error) {
	r.acquired++
	return r.permit, r.err
}
func (r *statsRuntimeRepo) ReadSiteAccountStats(context.Context, *ports.Actor, uuid.UUID) (*ports.SiteAccountStatsView, error) {
	r.reads++
	return r.view, r.err
}

type statsRuntimePermit struct {
	work                               ports.SiteAccountStatsExecution
	reserved, checks, finished, closed int
	reserveErr, checkErr, finishErr    error
	sample                             *ports.SiteAccountStats
	failure                            error
}

func (p *statsRuntimePermit) Snapshot() ports.SiteAccountStatsExecution { return p.work }
func (p *statsRuntimePermit) Reserve(context.Context) error             { p.reserved++; return p.reserveErr }
func (p *statsRuntimePermit) Check(context.Context) error               { p.checks++; return p.checkErr }
func (p *statsRuntimePermit) Finish(_ context.Context, s *ports.SiteAccountStats, e error) error {
	p.finished++
	p.sample = s
	p.failure = e
	return p.finishErr
}
func (p *statsRuntimePermit) Close() { p.closed++ }

type statsRuntimeClient struct {
	read  func(context.Context, string) (ports.SiteAccountStats, error)
	calls int
	uid   string
}

func (c *statsRuntimeClient) ReadAccountStats(ctx context.Context, uid string) (ports.SiteAccountStats, error) {
	c.calls++
	c.uid = uid
	return c.read(ctx, uid)
}

type statsRuntimeBuilder struct {
	client ports.SiteAccountStatsClient
	err    error
	calls  int
	work   ports.SiteAccountStatsExecution
}

func (b *statsRuntimeBuilder) NewSiteAccountStatsClient(w ports.SiteAccountStatsExecution) (ports.SiteAccountStatsClient, error) {
	b.calls++
	b.work = w
	return b.client, b.err
}
func statsRuntimeFixture() (ports.JobLease, *statsRuntimeRepo, *statsRuntimeBuilder, *statsRuntimeClient, ports.SiteAccountStats) {
	_, _, _, cfg := accountStatsConfigFixture()
	bonus := "42.0"
	sample := ports.SiteAccountStats{AccountID: "42", Uploaded: &indexer.SiteAccountAmount{Value: "1.00", Unit: "GB", Approximate: true}, Downloaded: &indexer.SiteAccountAmount{Value: "0.00", Unit: "KB", Approximate: true}, Ratio: indexer.SiteAccountRatio{State: indexer.SiteRatioFinite, Value: "1.000"}, Bonus: &bonus}
	p := &statsRuntimePermit{work: ports.SiteAccountStatsExecution{Config: *cfg.config, SourceURL: "https://owned-source.invalid/pt", Cookie: "owned-fixture-cookie"}}
	r := &statsRuntimeRepo{permit: p}
	c := &statsRuntimeClient{read: func(context.Context, string) (ports.SiteAccountStats, error) { return sample, nil }}
	b := &statsRuntimeBuilder{client: c}
	return ports.JobLease{ID: uuid.New(), Owner: "owned-worker", Token: 1}, r, b, c, sample
}

func TestAccountStatsCollectorSingleReadAndImmutableResult(t *testing.T) {
	lease, r, b, c, original := statsRuntimeFixture()
	if e := site.NewAccountStatsCollector(r, b).Execute(t.Context(), lease); e != nil {
		t.Fatal(e)
	}
	p := r.permit
	if r.acquired != 1 || b.calls != 1 || c.calls != 1 || c.uid != "42" || p.reserved != 1 || p.checks != 2 || p.finished != 1 || p.closed != 1 || p.sample == nil || p.failure != nil {
		t.Fatal("single controlled read did not finish exactly once")
	}
	if b.work.Config != p.work.Config || b.work.SourceURL != p.work.SourceURL || b.work.Cookie != p.work.Cookie {
		t.Fatal("builder did not receive frozen source")
	}
	*original.Bonus = "99.0"
	original.Uploaded.Value = "99.00"
	if *p.sample.Bonus != "42.0" || p.sample.Uploaded.Value != "1.00" || p.sample.Ratio.Value != "1.000" {
		t.Fatal("sample aliases client result or ratio was recomputed")
	}
}

func TestAccountStatsCollectorFailureDoesNotPublishSample(t *testing.T) {
	for name, upstream := range map[string]error{"authentication": ports.ErrAuthRequired, "blocked": ports.ErrBlocked, "malformed": ports.ErrBadResponse, "rate_limited": ports.NewRateLimitError(4*time.Second, "raw_cookie=private"), "unknown": errors.New("https://private.invalid?cookie=private")} {
		t.Run(name, func(t *testing.T) {
			lease, r, b, c, _ := statsRuntimeFixture()
			c.read = func(context.Context, string) (ports.SiteAccountStats, error) {
				return ports.SiteAccountStats{}, upstream
			}
			if e := site.NewAccountStatsCollector(r, b).Execute(t.Context(), lease); e != nil {
				t.Fatal(e)
			}
			p := r.permit
			if p.sample != nil || p.failure == nil || p.finished != 1 || p.closed != 1 || strings.Contains(p.failure.Error(), "private") {
				t.Fatal("failed read published sample or unsafe error")
			}
			if limit, ok := ports.IsRateLimited(upstream); ok {
				got, found := ports.IsRateLimited(p.failure)
				if !found || got.RetryAfter != limit.RetryAfter {
					t.Fatal("lost safe rate limit delay")
				}
			}
		})
	}
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid_precision", true: "wrong_account"}[wrong], func(t *testing.T) {
			lease, r, b, c, value := statsRuntimeFixture()
			if wrong {
				value.AccountID = "99"
			} else {
				value.Uploaded.Approximate = false
			}
			c.read = func(context.Context, string) (ports.SiteAccountStats, error) { return value, nil }
			if e := site.NewAccountStatsCollector(r, b).Execute(t.Context(), lease); e != nil {
				t.Fatal(e)
			}
			if r.permit.sample != nil || r.permit.failure != ports.ErrBadResponse {
				t.Fatal("accepted mismatched or exact-byte claim")
			}
		})
	}
}

func TestAccountStatsCollectorPreconditionsAndBudget(t *testing.T) {
	for name, change := range map[string]func(*statsRuntimeRepo, *statsRuntimeBuilder){"disabled": func(r *statsRuntimeRepo, _ *statsRuntimeBuilder) { r.permit.work.Config.Enabled = false }, "missing_cookie": func(r *statsRuntimeRepo, _ *statsRuntimeBuilder) { r.permit.work.Cookie = "" }, "invalid_uid": func(r *statsRuntimeRepo, _ *statsRuntimeBuilder) { r.permit.work.Config.ExpectedAccountID = "042" }, "missing_source": func(r *statsRuntimeRepo, _ *statsRuntimeBuilder) { r.permit.work.SourceURL = "" }, "future_lease_fence": func(r *statsRuntimeRepo, _ *statsRuntimeBuilder) {
		r.permit.checkErr = ports.ErrEndpointVersionConflict
	}, "budget_denied": func(r *statsRuntimeRepo, _ *statsRuntimeBuilder) {
		r.permit.reserveErr = ports.NewRateLimitError(2*time.Second, "shared budget")
	}, "nil_client": func(_ *statsRuntimeRepo, b *statsRuntimeBuilder) { b.client = nil }, "builder_error": func(_ *statsRuntimeRepo, b *statsRuntimeBuilder) { b.err = errors.New("raw_cookie=private") }} {
		t.Run(name, func(t *testing.T) {
			lease, r, b, c, _ := statsRuntimeFixture()
			change(r, b)
			_ = site.NewAccountStatsCollector(r, b).Execute(t.Context(), lease)
			if c.calls != 0 || r.permit.closed != 1 || r.permit.sample != nil {
				t.Fatal("precondition caused read, leaked permit or sample")
			}
			if r.permit.failure != nil && strings.Contains(r.permit.failure.Error(), "private") {
				t.Fatal("unsafe builder error")
			}
			if name == "budget_denied" && (r.permit.finished != 1 || r.permit.reserved != 1) {
				t.Fatal("budget denial not recorded without HTTP")
			}
		})
	}
	lease, r, b, _, _ := statsRuntimeFixture()
	lease.Token = 0
	if e := site.NewAccountStatsCollector(r, b).Execute(t.Context(), lease); !errors.Is(e, ports.ErrRejected) || r.acquired != 0 {
		t.Fatal(e)
	}
}

func TestAccountStatsCollectorCancellationRetainsPhysicalPermit(t *testing.T) {
	lease, r, b, c, _ := statsRuntimeFixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, observedCancel, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	c.read = func(ctx context.Context, _ string) (ports.SiteAccountStats, error) {
		close(entered)
		<-ctx.Done()
		close(observedCancel)
		<-release
		return ports.SiteAccountStats{}, ctx.Err()
	}
	go func() { defer close(returned); done <- site.NewAccountStatsCollector(r, b).Execute(ctx, lease) }()
	t.Cleanup(func() {
		cancel()
		once.Do(func() { close(release) })
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Error("collector did not return after release")
		}
	})
	<-entered
	cancel()
	<-observedCancel
	select {
	case <-done:
		t.Fatal("logical cancellation released active call")
	default:
	}
	// Inspect close only after the completion channel establishes ordering.
	once.Do(func() { close(release) })
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if r.permit.closed != 1 || r.permit.finished != 0 {
		t.Fatal("canceled read wrote a result or leaked its permit")
	}
}

func TestAccountStatsReaderPrivacyAndSnapshotIsolation(t *testing.T) {
	_, r, _, _, sample := statsRuntimeFixture()
	a, id, _, cfg := accountStatsConfigFixture()
	r.permit.work.Config = *cfg.config
	now := time.Date(2026, 10, 20, 12, 1, 0, 0, time.UTC)
	r.view = &ports.SiteAccountStatsView{Config: *cfg.config, Stats: &sample, ObservedAt: &now, SampleConfigVersion: 1, SampleSourceVersion: 5, LastAttemptAt: &now, CollectionState: "succeeded"}
	s := site.NewAccountStatsReader(r)
	member := a
	member.Role = ports.RoleMember
	if _, e := s.Read(t.Context(), &member, id); !errors.Is(e, ports.ErrForbidden) || r.reads != 0 {
		t.Fatal("member observed private statistics")
	}
	view, e := s.Read(t.Context(), &a, id)
	if e != nil {
		t.Fatal(e)
	}
	view.Stats.Uploaded.Value = "99.00"
	*view.Stats.Bonus = "99.0"
	*view.ObservedAt = time.Time{}
	if r.view.Stats.Uploaded.Value != "1.00" || *r.view.Stats.Bonus != "42.0" || r.view.ObservedAt.IsZero() {
		t.Fatal("view aliases persisted source values")
	}
	if r.acquired != 0 {
		t.Fatal("read started collection")
	}
	r.view = nil
	if v, e := s.Read(t.Context(), &a, id); v != nil || e != nil {
		t.Fatal("missing config became zero sample")
	}
}

func TestAccountStatsReaderRejectsBrokenBindings(t *testing.T) {
	for name, change := range map[string]func(*ports.SiteAccountStatsView){"account": func(v *ports.SiteAccountStatsView) { v.Stats.AccountID = "99" }, "future_config_version": func(v *ports.SiteAccountStatsView) { v.SampleConfigVersion = 2 }, "future_source_version": func(v *ports.SiteAccountStatsView) { v.SampleSourceVersion = 6 }, "missing_observation_time": func(v *ports.SiteAccountStatsView) { v.ObservedAt = nil }, "unknown_status": func(v *ports.SiteAccountStatsView) { v.CollectionState = "pretend-success" }, "unsafe_failure": func(v *ports.SiteAccountStatsView) { v.FailureCode = "cookie=private" }, "fabricated_zero": func(v *ports.SiteAccountStatsView) {
		v.Stats = nil
		v.ObservedAt = nil
		v.SampleConfigVersion = 0
		v.SampleSourceVersion = 0
		v.Stale = false
	}} {
		t.Run(name, func(t *testing.T) {
			_, r, _, _, value := statsRuntimeFixture()
			a, id, _, cfg := accountStatsConfigFixture()
			now := time.Now()
			r.view = &ports.SiteAccountStatsView{Config: *cfg.config, Stats: &value, ObservedAt: &now, SampleConfigVersion: 1, SampleSourceVersion: 5, CollectionState: "succeeded"}
			change(r.view)
			if _, e := site.NewAccountStatsReader(r).Read(t.Context(), &a, id); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal(e)
			}
		})
	}
}
