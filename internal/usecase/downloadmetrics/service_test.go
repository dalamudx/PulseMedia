package downloadmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type testClock struct{ now time.Time }

func (c testClock) Now() time.Time                  { return c.now }
func (c testClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

type testRepo struct {
	permit   *testPermit
	err      error
	acquires atomic.Int32
}

func (r *testRepo) EnqueueDue(context.Context) error     { return nil }
func (r *testRepo) RecoverExpired(context.Context) error { return nil }
func (r *testRepo) Acquire(context.Context, ports.JobLease) (ports.DownloadGlobalRatesPermit, error) {
	r.acquires.Add(1)
	return r.permit, r.err
}

type testPermit struct {
	source                   ports.DownloadGlobalRatesSource
	checkErr, finishErr      error
	checks, finishes, closes atomic.Int32
	observation              ports.DownloadGlobalRatesObservation
}

func (p *testPermit) Snapshot() ports.DownloadGlobalRatesSource { return p.source }
func (p *testPermit) Check(context.Context) error               { p.checks.Add(1); return p.checkErr }
func (p *testPermit) Finish(_ context.Context, o ports.DownloadGlobalRatesObservation) error {
	p.observation = o
	p.finishes.Add(1)
	return p.finishErr
}
func (p *testPermit) Close() { p.closes.Add(1) }

type testBuilder struct {
	reader ports.DownloadGlobalRatesReader
	err    error
	calls  atomic.Int32
}

func (b *testBuilder) NewDownloadGlobalRatesReader(ports.DownloadGlobalRatesSource) (ports.DownloadGlobalRatesReader, error) {
	b.calls.Add(1)
	return b.reader, b.err
}

type testReader struct {
	read  func(context.Context) (ports.DownloadGlobalRates, error)
	calls atomic.Int32
}

func (r *testReader) ReadGlobalRates(ctx context.Context) (ports.DownloadGlobalRates, error) {
	r.calls.Add(1)
	return r.read(ctx)
}
func setupCollector() (*Service, *testRepo, *testPermit, *testBuilder, *testReader, ports.JobLease) {
	p := &testPermit{source: ports.DownloadGlobalRatesSource{ClientID: uuid.New(), ConfigStamp: strings.Repeat("a", 64), ClientType: "qbittorrent", URL: "http://owned.invalid", Username: "private-user", Password: "private-password"}}
	r := &testRepo{permit: p}
	reader := &testReader{read: func(context.Context) (ports.DownloadGlobalRates, error) {
		down, up := int64(0), int64(123)
		return ports.DownloadGlobalRates{DownloadBytesPerSecond: &down, UploadBytesPerSecond: &up}, nil
	}}
	builder := &testBuilder{reader: reader}
	return NewService(r, builder, testClock{time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC)}), r, p, builder, reader, ports.JobLease{ID: uuid.New(), Owner: "original-worker", Token: 7}
}

func TestCollectorRecordsOneNativeObservation(t *testing.T) {
	svc, _, p, b, r, lease := setupCollector()
	if err := svc.Execute(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if p.checks.Load() != 1 || b.calls.Load() != 1 || r.calls.Load() != 1 || p.finishes.Load() != 1 || p.closes.Load() != 1 {
		t.Fatal("collection repeated a stage")
	}
	o := p.observation
	if o.Failure != "" || o.Rates == nil || *o.Rates.DownloadBytesPerSecond != 0 || *o.Rates.UploadBytesPerSecond != 123 || !o.ObservedAt.Equal(svc.clock.Now()) {
		t.Fatal("incorrect observation", o)
	}
	raw, err := json.Marshal(p.source)
	if err != nil || string(raw) != "{}" {
		t.Fatal("private source serialized", err)
	}
}

func TestCollectorFailureDoesNotPublishValues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		failure ports.DownloadGlobalRatesFailure
		retry   time.Duration
	}{
		{"auth", ports.ErrAuthRequired, ports.DownloadGlobalRatesAuth, 0},
		{"timeout", ports.ErrTimeout, ports.DownloadGlobalRatesTimeout, 0},
		{"deadline", context.DeadlineExceeded, ports.DownloadGlobalRatesTimeout, 0},
		{"blocked", ports.ErrBlocked, ports.DownloadGlobalRatesBlocked, 0},
		{"malformed", ports.ErrBadResponse, ports.DownloadGlobalRatesBadResponse, 0},
		{"private_error", errors.New("private URL/token"), ports.DownloadGlobalRatesUnavailable, 0},
		{"rate_limit", ports.NewRateLimitError(time.Minute, "private URL/token"), ports.DownloadGlobalRatesLimited, time.Minute},
		{"negative_retry", ports.NewRateLimitError(-time.Second, "private"), ports.DownloadGlobalRatesLimited, 0},
		{"huge_retry", ports.NewRateLimitError(48*time.Hour, "private"), ports.DownloadGlobalRatesLimited, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, p, _, r, lease := setupCollector()
			r.read = func(context.Context) (ports.DownloadGlobalRates, error) {
				n := int64(789)
				return ports.DownloadGlobalRates{DownloadBytesPerSecond: &n, UploadBytesPerSecond: &n}, tc.err
			}
			if err := svc.Execute(t.Context(), lease); err != nil {
				t.Fatal("failure observation was not sealed", err)
			}
			if p.observation.Rates != nil || p.observation.Failure != tc.failure || p.observation.RetryAfter != tc.retry || p.closes.Load() != 1 || p.finishes.Load() != 1 || r.calls.Load() != 1 {
				t.Fatal("failure leaked values or lost safe classification", p.observation)
			}
		})
	}
}

func TestCollectorRejectsInvalidAuthorityBeforeIO(t *testing.T) {
	for _, kind := range []string{"lease", "source", "stamp", "type", "stale", "builder", "commit", "nil_reader", "negative", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			svc, repo, p, b, r, lease := setupCollector()
			switch kind {
			case "lease":
				lease.Token = 0
			case "source":
				p.source.ClientID = uuid.Nil
			case "stamp":
				p.source.ConfigStamp = "bad"
			case "type":
				p.source.ClientType = "unsupported"
			case "stale":
				p.checkErr = ports.ErrStaleDeliveryLease
			case "builder":
				b.err = errors.New("private factory secret")
			case "commit":
				p.finishErr = ports.ErrEndpointVersionConflict
			case "nil_reader":
				b.reader = nil
			case "negative":
				r.read = func(context.Context) (ports.DownloadGlobalRates, error) {
					n := int64(-1)
					return ports.DownloadGlobalRates{UploadBytesPerSecond: &n}, nil
				}
			case "unknown":
				r.read = func(context.Context) (ports.DownloadGlobalRates, error) { return ports.DownloadGlobalRates{}, nil }
			}
			err := svc.Execute(t.Context(), lease)
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("private error escaped")
			}
			switch kind {
			case "lease":
				if !errors.Is(err, ports.ErrRejected) || repo.acquires.Load() != 0 {
					t.Fatal("invalid lease reached store", err)
				}
			case "source", "stamp", "type", "stale":
				if err == nil || r.calls.Load() != 0 || b.calls.Load() != 0 || p.finishes.Load() != 0 || p.closes.Load() != 1 {
					t.Fatal("invalid evidence reached I/O", err)
				}
			case "builder", "nil_reader":
				if err != nil || r.calls.Load() != 0 || p.observation.Failure != ports.DownloadGlobalRatesUnavailable || p.observation.Rates != nil {
					t.Fatal("builder failure was not unknown", err)
				}
			case "commit":
				if !errors.Is(err, ports.ErrEndpointVersionConflict) || p.closes.Load() != 1 {
					t.Fatal("stale commit accepted", err)
				}
			case "negative":
				if err != nil || p.observation.Rates != nil || p.observation.Failure != ports.DownloadGlobalRatesBadResponse {
					t.Fatal("negative native value published", err)
				}
			case "unknown":
				if err != nil || p.observation.Rates == nil || !reflect.DeepEqual(*p.observation.Rates, ports.DownloadGlobalRates{}) {
					t.Fatal("unknown values turned into zero", err)
				}
			}
		})
	}
}

func TestCollectorCancellationWaitsForPhysicalReturn(t *testing.T) {
	svc, _, p, _, r, lease := setupCollector()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	r.read = func(context.Context) (ports.DownloadGlobalRates, error) {
		close(entered)
		<-release
		n := int64(99)
		return ports.DownloadGlobalRates{DownloadBytesPerSecond: &n}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	go func() { done <- svc.Execute(ctx, lease) }()
	released, joined := false, false
	defer func() {
		cancel()
		if !released {
			close(release)
		}
		if !joined {
			<-done
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native call did not start")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		t.Fatal("exited before physical return", err)
	case <-time.After(20 * time.Millisecond):
	}
	if p.closes.Load() != 0 || p.finishes.Load() != 0 {
		t.Fatal("cancellation released source or published early")
	}
	close(release)
	released = true
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not join physical return")
	}
	if p.closes.Load() != 1 || p.finishes.Load() != 0 {
		t.Fatal("late success was published after cancellation")
	}
}
