package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestSiteAccountStatsPhysicalWaitsForExecution(t *testing.T) {
	for _, mode := range []string{"shutdown", "heartbeat_failure", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
			}
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			returned := make(chan error, 1)
			finished := make(chan struct{})
			var held atomic.Bool
			lost := errors.New("lease lost")
			physicalErr := errors.New("physical call ended")
			go func() {
				defer close(finished)
				returned <- runSiteAccountStatsPhysical(ctx, func(context.Context) error {
					if mode == "heartbeat_failure" {
						return lost
					}
					return nil
				}, func(workCtx context.Context) error {
					held.Store(true)
					defer held.Store(false)
					close(started)
					<-workCtx.Done()
					close(canceled)
					<-release // Model native I/O that has not physically honored cancellation yet.
					return physicalErr
				}, time.Millisecond)
			}()
			released := false
			defer func() {
				cancel()
				if !released {
					close(release)
				}
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("owned execution did not drain")
				}
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("execution did not start")
			}
			if mode == "shutdown" {
				cancel()
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("execution did not observe cancellation")
			}
			select {
			case err := <-returned:
				t.Fatalf("returned before physical completion: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if !held.Load() {
				t.Fatal("physical resource released before I/O returned")
			}
			close(release)
			released = true
			select {
			case err := <-returned:
				want := error(context.Canceled)
				switch mode {
				case "deadline":
					want = context.DeadlineExceeded
				case "heartbeat_failure":
					want = lost
				}
				if !errors.Is(err, want) || !errors.Is(err, physicalErr) || held.Load() {
					t.Fatalf("physical drain or joined error lost: %v, held=%v", err, held.Load())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("execution did not drain after release")
			}
		})
	}
}

func TestSiteAccountStatsPhysicalBudget(t *testing.T) {
	var observed context.Context
	err := runSiteAccountStatsPhysical(context.Background(), func(context.Context) error {
		t.Error("unexpected renewal")
		return nil
	}, func(ctx context.Context) error {
		observed = ctx
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 59*time.Second || time.Until(deadline) > time.Minute {
			t.Error("execution must receive its own 60 second budget")
		}
		return nil
	}, siteAccountStatsHeartbeat)
	if err != nil || observed.Err() != context.Canceled {
		t.Fatalf("completion did not cancel child: %v", err)
	}
	if siteAccountStatsLease != 2*time.Minute || siteAccountStatsHeartbeat != 15*time.Second {
		t.Fatal("unexpected lease or heartbeat")
	}
}

type siteAccountStatsTestTransport func(*http.Request) (*http.Response, error)

func (f siteAccountStatsTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type siteAccountStatsTestClock struct{}

func (siteAccountStatsTestClock) Now() time.Time { return time.Unix(1700000000, 0) }

func TestSiteAccountStatsBuilderSnapshot(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: siteAccountStatsTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://snapshot.example/pt/index.php" || r.Method != http.MethodGet || r.Header.Get("Cookie") != "session=snapshot" {
			t.Fatal("native request did not use the execution snapshot")
		}
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	builder := &siteAccountStatsClientBuilder{httpClient: client, clock: siteAccountStatsTestClock{}}
	snapshot := ports.SiteAccountStatsExecution{Config: ports.SiteAccountStatsConfig{Profile: ports.NexusPHPClassicAccountStatsProfile, ExpectedAccountID: "42"}, SourceURL: "https://snapshot.example/pt/", Cookie: "session=snapshot"}
	native, err := builder.NewSiteAccountStatsClient(snapshot)
	if err != nil || calls != 0 {
		t.Fatalf("constructor must perform no I/O: %v", err)
	}
	snapshot.SourceURL = "https://changed.example/"
	snapshot.Cookie = "session=changed"
	_, err = native.ReadAccountStats(context.Background(), "42")
	if !errors.Is(err, ports.ErrAuthRequired) || calls != 1 {
		t.Fatalf("expected one native GET, calls=%d err=%v", calls, err)
	}
}

func TestSiteAccountStatsBuilderRejectsInvalidConfig(t *testing.T) {
	builder := &siteAccountStatsClientBuilder{}
	for _, tc := range []struct{ profile, uid string }{
		{"", "42"}, {"unsupported", "42"},
		{ports.NexusPHPClassicAccountStatsProfile, ""},
		{ports.NexusPHPClassicAccountStatsProfile, "0"},
		{ports.NexusPHPClassicAccountStatsProfile, "42x"},
	} {
		native, err := builder.NewSiteAccountStatsClient(ports.SiteAccountStatsExecution{Config: ports.SiteAccountStatsConfig{Profile: tc.profile, ExpectedAccountID: tc.uid}})
		if native != nil || !errors.Is(err, ports.ErrRejected) {
			t.Fatalf("invalid profile/UID accepted: %v", err)
		}
	}
	native, err := builder.NewSiteAccountStatsClient(ports.SiteAccountStatsExecution{Config: ports.SiteAccountStatsConfig{Profile: ports.NexusPHPClassicAccountStatsProfile, ExpectedAccountID: "42"}})
	if err != nil || native == nil {
		t.Fatalf("nonblocking constructor rejected nil clock: %v", err)
	}
	if _, err := native.ReadAccountStats(context.Background(), "42"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("nil clock must fail safely before I/O: %v", err)
	}
}

func TestSiteAccountStatsBuilderMissingURL(t *testing.T) {
	builder := &siteAccountStatsClientBuilder{clock: siteAccountStatsTestClock{}, httpClient: &http.Client{Transport: siteAccountStatsTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("missing snapshot URL must not fall back to another source")
		return nil, ports.ErrUnavailable
	})}}
	native, err := builder.NewSiteAccountStatsClient(ports.SiteAccountStatsExecution{Config: ports.SiteAccountStatsConfig{Profile: ports.NexusPHPClassicAccountStatsProfile, ExpectedAccountID: "42"}, Cookie: "session=snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := native.ReadAccountStats(context.Background(), "42"); !errors.Is(err, ports.ErrRejected) {
		t.Fatalf("missing snapshot URL was accepted: %v", err)
	}
}

func (c siteAccountStatsTestClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }
