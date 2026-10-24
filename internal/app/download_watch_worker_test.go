package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type watchRepoFake struct {
	list    func(context.Context, ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error)
	resolve func(context.Context, ports.DownloadWatchBinding, ports.RootChangeHint, ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error)
}

func (r watchRepoFake) ListDownloadWatchSources(c context.Context, q ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
	return r.list(c, q)
}
func (r watchRepoFake) ResolveDownloadWatchTargets(c context.Context, b ports.DownloadWatchBinding, h ports.RootChangeHint, q ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
	if r.resolve == nil {
		return ports.DownloadWatchTargetPage{Complete: true}, nil
	}
	return r.resolve(c, b, h, q)
}

type watchObserverFake func(context.Context, ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error)

func (f watchObserverFake) ObserveDownloadWatchTarget(c context.Context, t ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	return f(c, t)
}

type watchRootFake struct {
	id     ports.FileIdentity
	marker bool
	closed atomic.Bool
}

func (r *watchRootFake) Identity(context.Context) (ports.FileIdentity, error) { return r.id, nil }
func (r *watchRootFake) Inspect(_ context.Context, p string) (ports.FileIdentity, error) {
	if p != ".pulsemedia_root" || !r.marker {
		return ports.FileIdentity{}, errors.New("missing")
	}
	return r.id, nil
}
func (r *watchRootFake) Close() { r.closed.Store(true) }

type watchFSFake struct {
	open  func(string) *watchRootFake
	watch func(context.Context, ports.RootWatchRequest, chan<- ports.RootChangeHint) error
}

func (f watchFSFake) OpenRoot(_ context.Context, p string) (ports.TransferRoot, error) {
	return f.open(p), nil
}
func (f watchFSFake) LockRoots(context.Context, ...string) (map[string]ports.TransferRoot, func(), error) {
	panic("watch must not acquire exclusive slots")
}
func (f watchFSFake) WatchRoot(c context.Context, r ports.RootWatchRequest, out chan<- ports.RootChangeHint) error {
	select {
	case out <- ports.RootChangeHint{RelativePath: ".", Kind: ports.RootChangeRescan}:
	case <-c.Done():
		return c.Err()
	}
	if f.watch != nil {
		return f.watch(c, r, out)
	}
	<-c.Done()
	return c.Err()
}
func watchBinding() ports.DownloadWatchBinding {
	return ports.DownloadWatchBinding{Stamp: "source-v1", ClientID: uuid.New(), ClientVersion: time.Now(), ClientStamp: "client-v1", SourceKind: "path_mapping", RemotePrefix: "/remote", LocalRoot: "/source", RootID: uuid.New(), Generation: 1, RootPath: "/library", BaselineScanID: uuid.New(), RootIdentity: ports.FileIdentity{Device: 1, Inode: 2}}
}
func watchFS() watchFSFake {
	return watchFSFake{open: func(p string) *watchRootFake {
		id := ports.FileIdentity{Device: 1, Inode: 3}
		if p == "/library" {
			id.Inode = 2
		}
		return &watchRootFake{id: id, marker: true}
	}}
}
func watchRepo(b ports.DownloadWatchBinding) watchRepoFake {
	return watchRepoFake{list: func(context.Context, ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
		return ports.DownloadWatchSourcePage{Sources: []ports.DownloadWatchSource{{Binding: b}}, Complete: true}, nil
	}}
}
func watchTarget(b ports.DownloadWatchBinding) ports.DownloadWatchTarget {
	return ports.DownloadWatchTarget{Binding: b, TaskID: uuid.New(), OperationID: uuid.New()}
}
func watchReceive[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("owned worker did not progress")
		var z T
		return z
	}
}
func watchStart(t *testing.T, repo watchRepoFake, fs watchFSFake, obs watchObserverFake, o downloadWatchOptions) (context.CancelFunc, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	wake := make(chan struct{}, 1)
	if o.refresh == 0 {
		o.refresh = 10 * time.Millisecond
	}
	if o.timeout == 0 {
		o.timeout = time.Second
	}
	go func() { defer close(done); (*App)(nil).runDownloadWatch(ctx, repo, obs, fs, wake, o) }()
	t.Cleanup(func() { cancel(); watchReceive(t, done) })
	return cancel, done, wake
}
func watchNoObservation(context.Context, ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	return nil, nil
}

func TestDownloadWatchBaselineAndMarker(t *testing.T) {
	for _, mode := range []string{"identity", "marker", "library_source"} {
		t.Run(mode, func(t *testing.T) {
			b := watchBinding()
			fs := watchFS()
			base := fs.open
			if mode == "library_source" {
				b.SourceKind = "library_identity"
				b.RemotePrefix = ""
				b.LocalRoot = b.RootPath
			}
			fs.open = func(p string) *watchRootFake {
				r := base(p)
				if p == b.RootPath {
					if mode == "marker" {
						r.marker = false
					} else {
						r.id.Inode = 99
					}
				}
				return r
			}
			reports := make(chan string, 32)
			var calls atomic.Int32
			cancel, done, _ := watchStart(t, watchRepo(b), fs, func(context.Context, ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
				calls.Add(1)
				return nil, nil
			}, downloadWatchOptions{report: func(s string) {
				select {
				case reports <- s:
				default:
				}
			}})
			reason := watchReceive(t, reports)
			if reason != "library_identity_changed" && reason != "library_marker_missing" {
				t.Fatal(reason)
			}
			cancel()
			watchReceive(t, done)
			if calls.Load() != 0 {
				t.Fatal("unsafe root observed")
			}
		})
	}
}

func TestDownloadWatchDedupeAndWakeAfterReturn(t *testing.T) {
	b := watchBinding()
	target := watchTarget(b)
	repo := watchRepo(b)
	repo.resolve = func(context.Context, ports.DownloadWatchBinding, ports.RootChangeHint, ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
		return ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{target, target}, Complete: true}, nil
	}
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var calls atomic.Int32
	_, _, wake := watchStart(t, repo, watchFS(), func(c context.Context, _ ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-c.Done():
			return nil, c.Err()
		}
		return &ports.DownloadExecutionTask{State: "completed", OrganizationState: "queued"}, nil
	}, downloadWatchOptions{refresh: time.Hour})
	watchReceive(t, entered)
	select {
	case <-wake:
		t.Fatal("wake before completion transaction returned")
	default:
	}
	close(release)
	watchReceive(t, wake)
	if calls.Load() != 1 {
		t.Fatalf("duplicate observer calls: %d", calls.Load())
	}
}

func TestDownloadWatchNoWakeForNullErrorOrTracking(t *testing.T) {
	for _, mode := range []string{"null", "error", "tracking"} {
		t.Run(mode, func(t *testing.T) {
			b := watchBinding()
			repo := watchRepo(b)
			target := watchTarget(b)
			repo.resolve = func(context.Context, ports.DownloadWatchBinding, ports.RootChangeHint, ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
				return ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{target}, Complete: true}, nil
			}
			returned := make(chan struct{})
			cancel, done, wake := watchStart(t, repo, watchFS(), func(context.Context, ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
				defer close(returned)
				switch mode {
				case "null":
					return nil, nil
				case "error":
					return &ports.DownloadExecutionTask{OrganizationState: "queued"}, errors.New("failed")
				}
				return &ports.DownloadExecutionTask{State: "tracking", TotalBytes: 10, DownloadedBytes: 9}, nil
			}, downloadWatchOptions{refresh: time.Hour})
			watchReceive(t, returned)
			cancel()
			watchReceive(t, done)
			select {
			case <-wake:
				t.Fatal("unexpected organization wake")
			default:
			}
		})
	}
}

func TestDownloadWatchSourcePaginationFairBeyond64(t *testing.T) {
	b := watchBinding()
	var mu sync.Mutex
	seen := map[string]bool{}
	all := make(chan struct{})
	var once sync.Once
	repo := watchRepo(b)
	repo.list = func(_ context.Context, q ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
		start, _ := strconv.Atoi(q.Cursor)
		end := min(start+q.Limit, 70)
		p := ports.DownloadWatchSourcePage{Complete: end == 70}
		for i := start; i < end; i++ {
			next := b
			next.Stamp = fmt.Sprintf("s%d", i)
			p.Sources = append(p.Sources, ports.DownloadWatchSource{Binding: next})
		}
		if end < 70 {
			p.NextCursor = strconv.Itoa(end)
		}
		return p, nil
	}
	repo.resolve = func(_ context.Context, b ports.DownloadWatchBinding, _ ports.RootChangeHint, _ ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
		mu.Lock()
		seen[b.Stamp] = true
		if len(seen) == 70 {
			once.Do(func() { close(all) })
		}
		mu.Unlock()
		return ports.DownloadWatchTargetPage{Complete: true}, nil
	}
	watchStart(t, repo, watchFS(), watchNoObservation, downloadWatchOptions{watchers: 3, refresh: 4 * time.Millisecond})
	watchReceive(t, all)
}

func TestDownloadWatchBindingChangeRejectsLateResolve(t *testing.T) {
	b := watchBinding()
	repo := watchRepo(b)
	var version atomic.Int32
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var observed atomic.Int32
	repo.list = func(context.Context, ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
		next := b
		if version.Load() != 0 {
			next.Stamp = "source-v2"
		}
		return ports.DownloadWatchSourcePage{Sources: []ports.DownloadWatchSource{{Binding: next}}, Complete: true}, nil
	}
	repo.resolve = func(c context.Context, src ports.DownloadWatchBinding, _ ports.RootChangeHint, _ ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
		if src.Stamp == b.Stamp {
			once.Do(func() { close(entered) })
			<-c.Done()
			close(canceled)
			<-release
			return ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{watchTarget(src)}, Complete: true}, nil
		}
		return ports.DownloadWatchTargetPage{Complete: true}, nil
	}
	watchStart(t, repo, watchFS(), func(context.Context, ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
		observed.Add(1)
		return nil, nil
	}, downloadWatchOptions{})
	watchReceive(t, entered)
	version.Store(1)
	watchReceive(t, canceled)
	close(release)
	// Queue a subsequent metadata response as a barrier after the stale response.
	time.Sleep(20 * time.Millisecond)
	if observed.Load() != 0 {
		t.Fatal("late stale binding dispatched")
	}
}

func TestDownloadWatchShutdownWaitsActualWatchAndObserver(t *testing.T) {
	b := watchBinding()
	repo := watchRepo(b)
	target := watchTarget(b)
	repo.resolve = func(context.Context, ports.DownloadWatchBinding, ports.RootChangeHint, ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
		return ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{target}, Complete: true}, nil
	}
	nativeEntered := make(chan *watchRootFake, 1)
	nativeCanceled := make(chan struct{})
	observerEntered := make(chan struct{})
	observerCanceled := make(chan struct{})
	releaseNative := make(chan struct{})
	releaseObserver := make(chan struct{})
	fs := watchFS()
	fs.watch = func(c context.Context, r ports.RootWatchRequest, _ chan<- ports.RootChangeHint) error {
		nativeEntered <- r.Root.(*watchRootFake)
		<-c.Done()
		close(nativeCanceled)
		<-releaseNative
		return c.Err()
	}
	cancel, done, _ := watchStart(t, repo, fs, func(c context.Context, _ ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
		close(observerEntered)
		<-c.Done()
		close(observerCanceled)
		<-releaseObserver
		return nil, c.Err()
	}, downloadWatchOptions{refresh: time.Hour})
	root := watchReceive(t, nativeEntered)
	watchReceive(t, observerEntered)
	cancel()
	watchReceive(t, nativeCanceled)
	watchReceive(t, observerCanceled)
	if root.closed.Load() {
		t.Fatal("capability closed before native return")
	}
	select {
	case <-done:
		t.Fatal("owner returned before physical callbacks")
	default:
	}
	close(releaseNative)
	select {
	case <-done:
		t.Fatal("owner returned before observer")
	default:
	}
	close(releaseObserver)
	watchReceive(t, done)
	if !root.closed.Load() {
		t.Fatal("capability not closed after join")
	}
}

func TestDownloadWatchNativeErrorCancelsObservationAndPinsReplacement(t *testing.T) {
	b := watchBinding()
	repo := watchRepo(b)
	target := watchTarget(b)
	repo.resolve = func(context.Context, ports.DownloadWatchBinding, ports.RootChangeHint, ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
		return ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{target}, Complete: true}, nil
	}
	var inode atomic.Int64
	inode.Store(3)
	fs := watchFS()
	base := fs.open
	fs.open = func(p string) *watchRootFake {
		r := base(p)
		if p == b.LocalRoot {
			r.id.Inode = inode.Load()
		}
		return r
	}
	fail := make(chan struct{})
	nativeStarts := make(chan struct{}, 8)
	fs.watch = func(c context.Context, _ ports.RootWatchRequest, _ chan<- ports.RootChangeHint) error {
		nativeStarts <- struct{}{}
		select {
		case <-fail:
			return ports.ErrFileWatchCoverageLost
		case <-c.Done():
			return c.Err()
		}
	}
	entered := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	reports := make(chan string, 64)
	cancel, done, _ := watchStart(t, repo, fs, func(c context.Context, _ ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
		entered <- struct{}{}
		<-c.Done()
		canceled <- struct{}{}
		return nil, c.Err()
	}, downloadWatchOptions{report: func(s string) {
		select {
		case reports <- s:
		default:
		}
	}})
	watchReceive(t, nativeStarts)
	watchReceive(t, entered)
	inode.Store(99)
	close(fail)
	watchReceive(t, canceled)
	for watchReceive(t, reports) != "source_identity_changed" {
	}
	cancel()
	watchReceive(t, done)
	select {
	case <-nativeStarts:
		t.Fatal("same stamp rebound replacement inode")
	default:
	}
}

func TestDownloadWatchQueueSaturationRebuildsSameIdentity(t *testing.T) {
	b := watchBinding()
	repo := watchRepo(b)
	resolving := make(chan struct{})
	var once sync.Once
	repo.resolve = func(c context.Context, _ ports.DownloadWatchBinding, _ ports.RootChangeHint, _ ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
		once.Do(func() { close(resolving) })
		<-c.Done()
		return ports.DownloadWatchTargetPage{}, c.Err()
	}
	reports := make(chan string, 128)
	started := make(chan ports.FileIdentity, 16)
	fs := watchFS()
	fs.watch = func(c context.Context, r ports.RootWatchRequest, out chan<- ports.RootChangeHint) error {
		started <- r.FrozenIdentity
		<-resolving
		for i := 0; i < 8; i++ {
			select {
			case out <- ports.RootChangeHint{RelativePath: "file", Kind: ports.RootChangeCloseWrite}:
			case <-c.Done():
				return c.Err()
			}
		}
		<-c.Done()
		return c.Err()
	}
	cancel, done, _ := watchStart(t, repo, fs, watchNoObservation, downloadWatchOptions{hints: 1, report: func(s string) {
		select {
		case reports <- s:
		default:
		}
	}})
	first := watchReceive(t, started)
	for watchReceive(t, reports) != "hint_queue_full" {
	}
	next := watchReceive(t, started)
	if first != next {
		t.Fatal("rescan rebound identity")
	}
	cancel()
	watchReceive(t, done)
}
func TestDownloadWatchTargetPaginationResumesAtLimit(t *testing.T) {
	b := watchBinding()
	repo := watchRepo(b)
	seen := make(chan string, 80)
	var mu sync.Mutex
	ops := make([]ports.DownloadWatchTarget, 70)
	for i := range ops {
		ops[i] = watchTarget(b)
	}
	repo.resolve = func(_ context.Context, _ ports.DownloadWatchBinding, _ ports.RootChangeHint, q ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
		mu.Lock()
		defer mu.Unlock()
		index, _ := strconv.Atoi(q.Cursor)
		p := ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{ops[index]}, Complete: index == 69}
		if index < 69 {
			p.NextCursor = strconv.Itoa(index + 1)
		}
		return p, nil
	}
	_, _, _ = watchStart(t, repo, watchFS(), func(_ context.Context, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
		seen <- target.OperationID.String()
		return nil, nil
	}, downloadWatchOptions{pages: 1, pageSize: 1, refresh: 3 * time.Millisecond})
	unique := map[string]bool{}
	for len(unique) < 70 {
		unique[watchReceive(t, seen)] = true
	}
}

func TestDownloadWatchRebuildAfterMoveKeepsPhysicalIdentity(t *testing.T) {
	b := watchBinding()
	repo := watchRepo(b)
	started := make(chan ports.FileIdentity, 8)
	fail := make(chan struct{})
	var attempts atomic.Int32
	fs := watchFS()
	fs.watch = func(c context.Context, r ports.RootWatchRequest, _ chan<- ports.RootChangeHint) error {
		started <- r.FrozenIdentity
		if attempts.Add(1) == 1 {
			select {
			case <-fail:
				return ports.ErrFileWatchCoverageLost
			case <-c.Done():
				return c.Err()
			}
		}
		<-c.Done()
		return c.Err()
	}
	cancel, done, _ := watchStart(t, repo, fs, watchNoObservation, downloadWatchOptions{})
	first := watchReceive(t, started)
	close(fail)
	second := watchReceive(t, started)
	if !sameDownloadWatchIdentity(first, second) {
		t.Fatal("rebuild did not retain moved directory identity")
	}
	cancel()
	watchReceive(t, done)
}
