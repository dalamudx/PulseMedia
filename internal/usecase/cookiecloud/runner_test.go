package cookiecloud_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	cloud "github.com/dalamudx/PulseMedia/internal/usecase/cookiecloud"
	"github.com/google/uuid"
)

type syncPermit struct {
	execution                   ports.CookieCloudExecution
	checks, reserves, finishes  int
	closed                      atomic.Bool
	reserveError, errorAtFinish error
	checkErrors                 []error
	backup                      *ports.CookieCloudBackup
	failure                     error
}

func (p *syncPermit) Snapshot() ports.CookieCloudExecution { return p.execution }
func (p *syncPermit) Reserve(context.Context) error        { p.reserves++; return p.reserveError }
func (p *syncPermit) Check(context.Context) error {
	p.checks++
	if p.checks <= len(p.checkErrors) {
		return p.checkErrors[p.checks-1]
	}
	return nil
}
func (p *syncPermit) Finish(_ context.Context, b *ports.CookieCloudBackup, failure error) error {
	p.finishes++
	p.backup = b
	p.failure = failure
	return p.errorAtFinish
}
func (p *syncPermit) Close() { p.closed.Store(true) }

type syncRepository struct {
	permit   *syncPermit
	acquires int
	err      error
}

func (*syncRepository) EnqueueDue(context.Context) error     { return nil }
func (*syncRepository) RecoverExpired(context.Context) error { return nil }
func (r *syncRepository) Acquire(context.Context, ports.JobLease) (ports.CookieCloudSyncPermit, error) {
	r.acquires++
	if r.err != nil {
		return nil, r.err
	}
	return r.permit, nil
}

type syncReader struct {
	calls atomic.Int32
	read  func(context.Context) (*ports.CookieCloudBackup, error)
}

func (r *syncReader) ReadBackup(ctx context.Context, url, key, password string) (*ports.CookieCloudBackup, error) {
	r.calls.Add(1)
	if url != "https://cloud.example/private" || key != "owned-key" || password != "owned-password" {
		return nil, ports.ErrRejected
	}
	return r.read(ctx)
}
func syncFixture() (*syncRepository, *syncPermit, *syncReader, ports.JobLease) {
	p := &syncPermit{execution: ports.CookieCloudExecution{Config: ports.CookieCloudConfigView{OwnerID: uuid.New(), Version: 1, Enabled: true, HasConnection: true}, Connection: ports.CookieCloudConnection{ServerURL: "https://cloud.example/private", UserKey: "owned-key", Password: "owned-password"}}}
	r := &syncReader{read: func(context.Context) (*ports.CookieCloudBackup, error) {
		return &ports.CookieCloudBackup{Cookies: map[string][]ports.CookieCloudCookie{}}, nil
	}}
	return &syncRepository{permit: p}, p, r, ports.JobLease{ID: uuid.New(), Owner: "owned-worker", Token: 1}
}

func TestCookieCloudRunnerSingleReadAndEmptyPreservation(t *testing.T) {
	repo, p, reader, lease := syncFixture()
	err := cloud.NewRunner(repo, reader).Execute(t.Context(), lease)
	if err != nil || reader.calls.Load() != 1 || p.checks != 2 || p.reserves != 1 || p.finishes != 1 || p.backup == nil || p.failure != nil || !p.closed.Load() {
		t.Fatal("single read and empty-result preservation contract changed", err)
	}
}

func TestCookieCloudRunnerRejectsBeforeNetwork(t *testing.T) {
	for _, kind := range []string{"invalid-lease", "cancelled", "disabled", "missing-secret", "budget", "preflight", "acquire"} {
		t.Run(kind, func(t *testing.T) {
			repo, p, reader, lease := syncFixture()
			ctx := t.Context()
			switch kind {
			case "invalid-lease":
				lease.Token = 0
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "disabled":
				p.execution.Config.Enabled = false
			case "missing-secret":
				p.execution.Connection.Password = ""
			case "budget":
				p.reserveError = ports.NewRateLimitError(time.Minute, "owned-private-detail")
			case "preflight":
				p.checkErrors = []error{ports.ErrCookieCloudSyncLeaseLost}
			case "acquire":
				repo.err = ports.ErrForbidden
			}
			err := cloud.NewRunner(repo, reader).Execute(ctx, lease)
			if err == nil || reader.calls.Load() != 0 || p.finishes != 0 {
				t.Fatal("invalid authority reached native read or positive finish")
			}
			if repo.acquires == 1 && repo.err == nil && !p.closed.Load() {
				t.Fatal("acquired permit leaked")
			}
		})
	}
}

func TestCookieCloudRunnerLateAuthorityRejectsPositiveBackup(t *testing.T) {
	for _, failure := range []error{ports.ErrCookieCloudSyncLeaseLost, ports.ErrCookieCloudConfigConflict, ports.ErrForbidden} {
		repo, p, reader, lease := syncFixture()
		p.checkErrors = []error{nil, failure}
		if err := cloud.NewRunner(repo, reader).Execute(t.Context(), lease); !errors.Is(err, failure) || reader.calls.Load() != 1 || p.finishes != 0 || !p.closed.Load() {
			t.Fatal("late authority loss published data or leaked permit", err)
		}
	}
}

func TestCookieCloudRunnerCancellationWaitsForPhysicalRead(t *testing.T) {
	repo, p, reader, lease := syncFixture()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	reader.read = func(context.Context) (*ports.CookieCloudBackup, error) {
		close(entered)
		<-release
		return &ports.CookieCloudBackup{Cookies: map[string][]ports.CookieCloudCookie{}}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { done <- cloud.NewRunner(repo, reader).Execute(ctx, lease) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("native call did not start")
	}
	cancel()
	select {
	case <-done:
		close(release)
		t.Fatal("runner returned while native I/O was active")
	case <-time.After(20 * time.Millisecond):
	}
	if p.closed.Load() {
		close(release)
		<-done
		t.Fatal("physical exclusion released before native return")
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || p.finishes != 0 || !p.closed.Load() {
			t.Fatal("cancelled native result reached finish", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not join native return")
	}
}

func TestCookieCloudRunnerSafeFailureAndNoReplay(t *testing.T) {
	for _, kind := range []string{"transport", "timeout", "rate-limit", "nil", "commit"} {
		t.Run(kind, func(t *testing.T) {
			repo, p, reader, lease := syncFixture()
			want := ports.ErrUnavailable
			switch kind {
			case "transport":
				reader.read = func(context.Context) (*ports.CookieCloudBackup, error) {
					return nil, errors.New("owned-password private transport")
				}
			case "timeout":
				want = ports.ErrTimeout
				reader.read = func(context.Context) (*ports.CookieCloudBackup, error) { return nil, context.DeadlineExceeded }
			case "rate-limit":
				reader.read = func(context.Context) (*ports.CookieCloudBackup, error) {
					return nil, ports.NewRateLimitError(24*time.Hour, "owned-password")
				}
			case "nil":
				want = ports.ErrBadResponse
				reader.read = func(context.Context) (*ports.CookieCloudBackup, error) { return nil, nil }
			case "commit":
				p.errorAtFinish = ports.ErrCookieCloudSyncLeaseLost
				want = ports.ErrCookieCloudSyncLeaseLost
			}
			err := cloud.NewRunner(repo, reader).Execute(t.Context(), lease)
			if reader.calls.Load() != 1 || p.finishes != 1 || !p.closed.Load() || err == nil || strings.Contains(err.Error(), "owned-password") {
				t.Fatal("failure replayed or exposed secrets", err)
			}
			if kind == "rate-limit" {
				limit, ok := ports.IsRateLimited(err)
				if !ok || limit.RetryAfter != time.Hour {
					t.Fatal("rate limit bounds lost")
				}
			} else if !errors.Is(err, want) {
				t.Fatal("wrong safe failure classification", err)
			}
			if kind != "commit" && p.backup != nil {
				t.Fatal("failed native backup retained publication authority")
			}
		})
	}
}
