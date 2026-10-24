package download

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type upgradeWatchTestCapture struct {
	request ports.MovieUpgradeTrackingHintRequest
}

func (c *upgradeWatchTestCapture) MovieUpgradeTrackingHintRequest() ports.MovieUpgradeTrackingHintRequest {
	return c.request
}

type upgradeWatchTestScheduler struct {
	validates, calls, resolves int
	validate                   func() error
	schedule                   func(int) (*ports.MovieUpgradeTrackingHintScheduleResult, error)
	capture                    ports.MovieUpgradeTrackingHintCapture
}

func (s *upgradeWatchTestScheduler) ResolveMovieUpgradeTrackingHint(context.Context, ports.MovieUpgradeTrackingHintRequest) (ports.MovieUpgradeTrackingHintCapture, error) {
	s.resolves++
	return nil, ports.ErrUnavailable
}
func (s *upgradeWatchTestScheduler) ValidateMovieUpgradeTrackingHint(_ context.Context, c ports.MovieUpgradeTrackingHintCapture) error {
	s.validates++
	if c != s.capture {
		return ports.ErrForbidden
	}
	if s.validate != nil {
		return s.validate()
	}
	return nil
}
func (s *upgradeWatchTestScheduler) ScheduleMovieUpgradeTrackingHint(_ context.Context, c ports.MovieUpgradeTrackingHintCapture) (*ports.MovieUpgradeTrackingHintScheduleResult, error) {
	if c != s.capture {
		return nil, ports.ErrForbidden
	}
	s.calls++
	return s.schedule(s.calls)
}

type upgradeWatchTestFiles struct {
	opens, closes  int
	source, target ports.FileIdentity
	markerErr      error
	open           func(context.Context)
}
type upgradeWatchTestRoot struct {
	files *upgradeWatchTestFiles
	id    ports.FileIdentity
}

func (f *upgradeWatchTestFiles) OpenRoot(ctx context.Context, path string) (ports.TransferRoot, error) {
	f.opens++
	if f.open != nil {
		f.open(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := f.source
	if path == "/library" {
		id = f.target
	}
	return &upgradeWatchTestRoot{files: f, id: id}, nil
}
func (*upgradeWatchTestFiles) LockRoots(context.Context, ...string) (map[string]ports.TransferRoot, func(), error) {
	return nil, nil, ports.ErrUnavailable
}
func (r *upgradeWatchTestRoot) Identity(ctx context.Context) (ports.FileIdentity, error) {
	return r.id, ctx.Err()
}
func (r *upgradeWatchTestRoot) Inspect(ctx context.Context, _ string) (ports.FileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ports.FileIdentity{}, err
	}
	return ports.FileIdentity{Device: 1, Inode: 7}, r.files.markerErr
}
func (r *upgradeWatchTestRoot) Close() { r.files.closes++ }
func upgradeWatchTestTarget() (ports.DownloadWatchTarget, *upgradeWatchTestScheduler, *upgradeWatchTestFiles) {
	b := ports.DownloadWatchBinding{ClientID: uuid.New(), ClientVersion: time.Now(), ClientStamp: "config", Stamp: "binding", LocalRoot: "/source", RootPath: "/library", MarkerFilename: ".pulsemedia_root", RootIdentity: ports.FileIdentity{Device: 1, Inode: 6}}
	c := &upgradeWatchTestCapture{request: ports.MovieUpgradeTrackingHintRequest{OperationID: uuid.New(), TaskID: uuid.New(), PriorObservationID: uuid.New(), Binding: b, SourceIdentity: ports.FileIdentity{Device: 1, Inode: 5}}}
	target := ports.DownloadWatchTarget{Kind: "movie_upgrade_tracking_hint", OperationID: c.request.OperationID, TaskID: c.request.TaskID, ClientID: b.ClientID, ClientStamp: b.ClientStamp, ClientVersion: b.ClientVersion, Binding: b, MovieUpgradeHintCapture: c}
	s := &upgradeWatchTestScheduler{capture: c, schedule: func(int) (*ports.MovieUpgradeTrackingHintScheduleResult, error) {
		return &ports.MovieUpgradeTrackingHintScheduleResult{Queued: true, JobID: uuid.New()}, nil
	}}
	return target, s, &upgradeWatchTestFiles{source: c.request.SourceIdentity, target: b.RootIdentity}
}
func TestMovieUpgradeHintWaitsSamePredecessor(t *testing.T) {
	target, s, files := upgradeWatchTestTarget()
	wake := make(chan struct{}, 2)
	s.schedule = func(n int) (*ports.MovieUpgradeTrackingHintScheduleResult, error) {
		if n == 1 {
			return &ports.MovieUpgradeTrackingHintScheduleResult{RetryAfter: 5 * time.Millisecond}, nil
		}
		return &ports.MovieUpgradeTrackingHintScheduleResult{Queued: true, JobID: uuid.New()}, nil
	}
	if _, err := NewMovieUpgradeHintObserver(s, files, wake).ObserveDownloadWatchTarget(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if s.calls != 2 || s.validates != 2 || s.resolves != 0 || files.opens != 4 || files.closes != 4 || len(wake) != 1 {
		t.Fatalf("calls=%d validations=%d resolves=%d opens=%d closes=%d wakes=%d", s.calls, s.validates, s.resolves, files.opens, files.closes, len(wake))
	}
}
func TestMovieUpgradeHintCancellation(t *testing.T) {
	t.Run("before_dispatch", func(t *testing.T) {
		target, s, f := upgradeWatchTestTarget()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := NewMovieUpgradeHintObserver(s, f, nil).ObserveDownloadWatchTarget(ctx, target)
		if !errors.Is(err, context.Canceled) || s.calls != 0 || s.validates != 0 || f.opens != 0 {
			t.Fatal("cancelled dispatch entered dependency")
		}
	})
	t.Run("during_wait", func(t *testing.T) {
		target, s, f := upgradeWatchTestTarget()
		called := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		wake := make(chan struct{}, 1)
		s.schedule = func(int) (*ports.MovieUpgradeTrackingHintScheduleResult, error) {
			close(called)
			return &ports.MovieUpgradeTrackingHintScheduleResult{RetryAfter: 30 * time.Second}, nil
		}
		go func() {
			_, err := NewMovieUpgradeHintObserver(s, f, wake).ObserveDownloadWatchTarget(ctx, target)
			done <- err
		}()
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("initial validation did not return")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("wait ignored cancellation")
		}
		if s.calls != 1 || len(wake) != 0 || f.opens != f.closes {
			t.Fatal("cancelled wait created another opportunity or leaked root")
		}
	})
	t.Run("blocked_root_waits_actual_return", func(t *testing.T) {
		target, s, f := upgradeWatchTestTarget()
		entered := make(chan struct{})
		release := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		var once sync.Once
		f.open = func(context.Context) { close(entered); <-release }
		t.Cleanup(func() { cancel(); once.Do(func() { close(release) }) })
		go func() {
			_, err := NewMovieUpgradeHintObserver(s, f, nil).ObserveDownloadWatchTarget(ctx, target)
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("root read not entered")
		}
		cancel()
		select {
		case <-done:
			t.Fatal("logical cancellation returned before root I/O")
		case <-time.After(15 * time.Millisecond):
		}
		once.Do(func() { close(release) })
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("root call did not join")
		}
		if s.calls != 0 {
			t.Fatal("cancelled physical read queued work")
		}
	})
}
func TestMovieUpgradeHintBindingAndIssuer(t *testing.T) {
	for _, name := range []string{"kind", "operation", "root", "missing", "issuer"} {
		t.Run(name, func(t *testing.T) {
			target, s, f := upgradeWatchTestTarget()
			switch name {
			case "kind":
				target.Kind = "ordinary"
			case "operation":
				target.OperationID = uuid.New()
			case "root":
				target.Binding.RootPath = "/elsewhere"
			case "missing":
				target.MovieUpgradeHintCapture = nil
			case "issuer":
				s.validate = func() error { return ports.ErrForbidden }
			}
			_, err := NewMovieUpgradeHintObserver(s, f, nil).ObserveDownloadWatchTarget(t.Context(), target)
			if err == nil || f.opens != 0 || s.calls != 0 {
				t.Fatal("untrusted capture was used as a filesystem capability")
			}
		})
	}
}
func TestMovieUpgradeHintRootChangesDuringWait(t *testing.T) {
	for _, name := range []string{"source", "target", "marker"} {
		t.Run(name, func(t *testing.T) {
			target, s, f := upgradeWatchTestTarget()
			wake := make(chan struct{}, 1)
			s.schedule = func(int) (*ports.MovieUpgradeTrackingHintScheduleResult, error) {
				switch name {
				case "source":
					f.source.Inode++
				case "target":
					f.target.Inode++
				case "marker":
					f.markerErr = ports.ErrFileSymlink
				}
				return &ports.MovieUpgradeTrackingHintScheduleResult{RetryAfter: time.Millisecond}, nil
			}
			_, err := NewMovieUpgradeHintObserver(s, f, wake).ObserveDownloadWatchTarget(t.Context(), target)
			if !errors.Is(err, ports.ErrDownloadWatchStale) || s.calls != 1 || len(wake) != 0 || f.closes != f.opens {
				t.Fatalf("stale root was used after wait: calls=%d opens=%d closes=%d err=%v", s.calls, f.opens, f.closes, err)
			}
		})
	}
}
func TestMovieUpgradeHintSchedulerReplies(t *testing.T) {
	cases := []struct {
		name    string
		reply   *ports.MovieUpgradeTrackingHintScheduleResult
		wantErr bool
	}{
		{"nil", nil, true}, {"negative", &ports.MovieUpgradeTrackingHintScheduleResult{RetryAfter: -1}, true}, {"missing_job", &ports.MovieUpgradeTrackingHintScheduleResult{Queued: true}, true}, {"queued_wait", &ports.MovieUpgradeTrackingHintScheduleResult{Queued: true, JobID: uuid.New(), RetryAfter: 1}, true}, {"unclaimed_job", &ports.MovieUpgradeTrackingHintScheduleResult{JobID: uuid.New()}, true}, {"over_budget", &ports.MovieUpgradeTrackingHintScheduleResult{RetryAfter: 31 * time.Second}, true}, {"terminal", &ports.MovieUpgradeTrackingHintScheduleResult{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, s, f := upgradeWatchTestTarget()
			wake := make(chan struct{}, 1)
			s.schedule = func(int) (*ports.MovieUpgradeTrackingHintScheduleResult, error) { return tc.reply, nil }
			_, err := NewMovieUpgradeHintObserver(s, f, wake).ObserveDownloadWatchTarget(t.Context(), target)
			if (err != nil) != tc.wantErr || len(wake) != 0 || f.closes != f.opens {
				t.Fatal("malformed/terminal result granted a wake or leaked a root")
			}
		})
	}
}
