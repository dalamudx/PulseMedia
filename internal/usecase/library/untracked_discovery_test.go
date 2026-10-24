package library

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type discoveryTestCapture struct {
	snapshot ports.UntrackedDiscoverySnapshot
}

func (c *discoveryTestCapture) UntrackedDiscoverySnapshot() ports.UntrackedDiscoverySnapshot {
	return c.snapshot
}

type discoveryTestRepo struct {
	capture                                      *discoveryTestCapture
	loadErr, validateErr, recordErr, completeErr error
	batches                                      [][]ports.UntrackedFileObservation
	loaded, validated, completed                 int
	onRecord                                     func()
}

func (r *discoveryTestRepo) LoadUntrackedDiscovery(context.Context, ports.JobLease) (ports.UntrackedDiscoveryCapture, error) {
	r.loaded++
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	if r.capture == nil {
		return nil, nil
	}
	return r.capture, nil
}
func (r *discoveryTestRepo) ValidateUntrackedDiscovery(context.Context, ports.UntrackedDiscoveryCapture) error {
	r.validated++
	return r.validateErr
}
func (r *discoveryTestRepo) RecordUntrackedDiscovery(_ context.Context, _ ports.UntrackedDiscoveryCapture, b []ports.UntrackedFileObservation) error {
	if r.recordErr != nil {
		return r.recordErr
	}
	r.batches = append(r.batches, append([]ports.UntrackedFileObservation(nil), b...))
	if r.onRecord != nil {
		r.onRecord()
	}
	return nil
}
func (r *discoveryTestRepo) CompleteUntrackedDiscovery(context.Context, ports.UntrackedDiscoveryCapture) error {
	r.completed++
	return r.completeErr
}

type discoveryTestFS struct {
	identities             map[string]ports.FileIdentity
	entries                map[string]ports.FileIdentity
	source, target, marker string
	opens, walked          int
	closed, released       atomic.Int32
	walk                   func(context.Context, func(string, ports.FileIdentity) error) error
	lock                   func()
}
type discoveryTestRoot struct {
	fs       *discoveryTestFS
	path     string
	identity ports.FileIdentity
	closed   bool
}

func (r *discoveryTestRoot) Identity(ctx context.Context) (ports.FileIdentity, error) {
	return r.identity, ctx.Err()
}
func (r *discoveryTestRoot) Inspect(ctx context.Context, p string) (ports.FileIdentity, error) {
	if ctx.Err() != nil {
		return ports.FileIdentity{}, ctx.Err()
	}
	if r.path == r.fs.target && p == r.fs.marker {
		return ports.FileIdentity{Device: r.identity.Device, Inode: 777, Size: 4, Mtime: 1}, nil
	}
	if r.path == r.fs.source {
		if i, ok := r.fs.entries[p]; ok {
			return i, nil
		}
	}
	return ports.FileIdentity{}, ports.ErrFileIdentity
}
func (r *discoveryTestRoot) Close() {
	if !r.closed {
		r.closed = true
		r.fs.closed.Add(1)
	}
}
func (f *discoveryTestFS) OpenRoot(ctx context.Context, p string) (ports.TransferRoot, error) {
	f.opens++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	i, ok := f.identities[p]
	if !ok {
		return nil, ports.ErrFileIdentity
	}
	return &discoveryTestRoot{fs: f, path: p, identity: i}, nil
}
func (f *discoveryTestFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	if f.lock != nil {
		f.lock()
	}
	roots := make(map[string]ports.TransferRoot)
	release := func() {
		f.released.Add(1)
		for _, r := range roots {
			r.Close()
		}
	}
	for _, p := range paths {
		if roots[p] != nil {
			continue
		}
		r, err := f.OpenRoot(ctx, p)
		if err != nil {
			return roots, release, err
		}
		roots[p] = r
	}
	return roots, release, nil
}
func (f *discoveryTestFS) Walk(ctx context.Context, _ ports.TransferRoot, visit func(string, ports.FileIdentity) error) error {
	f.walked++
	if f.walk != nil {
		return f.walk(ctx, visit)
	}
	paths := make([]string, 0, len(f.entries))
	for p := range f.entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := visit(p, f.entries[p]); err != nil {
			return err
		}
	}
	return nil
}
func discoveryTestFixture(t *testing.T) (*UntrackedDiscoveryService, *discoveryTestRepo, *discoveryTestFS, ports.JobLease) {
	t.Helper()
	_, scopeRepo, _, _, _ := scopeTestFixture(t)
	scope := scopeTestReceipt(scopeRepo.capture.material, ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 6, Inode: 10}, TargetIdentity: ports.FileIdentity{Device: 7, Inode: 20}}, "discovery-scope").Scope
	lease := ports.JobLease{ID: uuid.New(), Owner: "owned-discovery", Token: 1}
	repo := &discoveryTestRepo{capture: &discoveryTestCapture{snapshot: ports.UntrackedDiscoverySnapshot{Lease: lease, Scope: scope, Sequence: 1}}}
	files := &discoveryTestFS{source: scope.LocalRoot, target: scope.TargetRootPath, marker: scope.TargetMarker, identities: map[string]ports.FileIdentity{scope.LocalRoot: scope.SourceIdentity, scope.TargetRootPath: scope.TargetIdentity}, entries: map[string]ports.FileIdentity{"Movie.mkv": {Device: 6, Inode: 30, Size: 100, Mtime: 1}}}
	return NewUntrackedDiscoveryService(repo, files), repo, files, lease
}

func TestUntrackedDiscoveryBatchesAndDirectoryChurn(t *testing.T) {
	s, r, f, lease := discoveryTestFixture(t)
	f.entries = make(map[string]ports.FileIdentity)
	for n := 0; n < 257; n++ {
		f.entries[fmt.Sprintf("folder/movie-%03d.mkv", n)] = ports.FileIdentity{Device: 6, Inode: int64(100 + n), Size: 100, Mtime: 1}
	}
	r.onRecord = func() { i := f.identities[f.source]; i.Size++; i.Mtime++; f.identities[f.source] = i }
	if err := s.Execute(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if len(r.batches) != 3 || len(r.batches[0]) != 128 || len(r.batches[1]) != 128 || len(r.batches[2]) != 1 || r.completed != 1 || f.released.Load() != 1 || int(f.closed.Load()) != f.opens {
		t.Fatal("unbounded batch, false identity change or leaked capability")
	}
}
func TestUntrackedDiscoveryInvalidAuthorityZeroFilesystem(t *testing.T) {
	for _, kind := range []string{"nil", "load", "lease", "token", "sequence", "disabled", "source", "target"} {
		t.Run(kind, func(t *testing.T) {
			s, r, f, lease := discoveryTestFixture(t)
			switch kind {
			case "nil":
				r.capture = nil
			case "load":
				r.loadErr = ports.ErrUntrackedDiscoveryStale
			case "lease":
				r.capture.snapshot.Lease.ID = uuid.New()
			case "token":
				r.capture.snapshot.Lease.Token++
			case "sequence":
				r.capture.snapshot.Sequence = 0
			case "disabled":
				r.capture.snapshot.Scope.Enabled = false
			case "source":
				r.capture.snapshot.Scope.SourceIdentity.Inode = 0
			case "target":
				r.capture.snapshot.Scope.TargetIdentity.Inode = 0
			}
			if err := s.Execute(t.Context(), lease); !errors.Is(err, ports.ErrUntrackedDiscoveryStale) {
				t.Fatal(err)
			}
			if f.opens != 0 || f.released.Load() != 0 || r.completed != 0 || len(r.batches) != 0 {
				t.Fatal("invalid authority reached filesystem or writeback")
			}
		})
	}
}
func TestUntrackedDiscoveryChangesRejectWriteback(t *testing.T) {
	for _, kind := range []string{"source", "target", "file", "lease"} {
		t.Run(kind, func(t *testing.T) {
			s, r, f, lease := discoveryTestFixture(t)
			f.walk = func(_ context.Context, visit func(string, ports.FileIdentity) error) error {
				old := f.entries["Movie.mkv"]
				switch kind {
				case "source":
					i := f.identities[f.source]
					i.Inode++
					f.identities[f.source] = i
				case "target":
					i := f.identities[f.target]
					i.Inode++
					f.identities[f.target] = i
				case "file":
					i := old
					i.Mtime++
					f.entries["Movie.mkv"] = i
				case "lease":
					r.validateErr = ports.ErrUntrackedDiscoveryStale
				}
				return visit("Movie.mkv", old)
			}
			expected := ports.ErrUntrackedDiscoveryStale
			if kind == "target" {
				expected = ports.ErrUnavailable
			}
			if err := s.Execute(t.Context(), lease); !errors.Is(err, expected) {
				t.Fatal(err)
			}
			if r.completed != 0 || len(r.batches) != 0 || f.released.Load() != 1 || int(f.closed.Load()) != f.opens {
				t.Fatal("changed evidence reached writeback or leaked capability")
			}
		})
	}
}
func TestUntrackedDiscoveryPartialTraversalNeverCompletes(t *testing.T) {
	s, r, f, lease := discoveryTestFixture(t)
	failure := errors.New("owned traversal interrupted")
	f.walk = func(_ context.Context, visit func(string, ports.FileIdentity) error) error {
		for n := 0; n < 128; n++ {
			p := fmt.Sprintf("part-%d.mkv", n)
			i := ports.FileIdentity{Device: 6, Inode: int64(n + 100), Size: 4, Mtime: 1}
			f.entries[p] = i
			if err := visit(p, i); err != nil {
				return err
			}
		}
		return failure
	}
	if err := s.Execute(t.Context(), lease); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(r.batches) != 1 || r.completed != 0 || f.released.Load() != 1 {
		t.Fatal("partial traversal certified complete or discarded valid observations")
	}
}
func TestUntrackedDiscoveryRejectsInvalidObservation(t *testing.T) {
	for _, relative := range []string{"", "../escape.mkv", "/absolute.mkv", "a//b.mkv", "a\\b.mkv", "bad\x00.mkv"} {
		t.Run(fmt.Sprintf("%q", relative), func(t *testing.T) {
			s, r, f, lease := discoveryTestFixture(t)
			f.walk = func(_ context.Context, visit func(string, ports.FileIdentity) error) error {
				return visit(relative, f.entries["Movie.mkv"])
			}
			if err := s.Execute(t.Context(), lease); !errors.Is(err, ports.ErrRejected) {
				t.Fatal(err)
			}
			if len(r.batches) != 0 || r.completed != 0 || f.released.Load() != 1 {
				t.Fatal("invalid path published")
			}
		})
	}
}
func TestUntrackedDiscoveryRepositoryFailuresReleaseRoots(t *testing.T) {
	for _, where := range []string{"record", "complete"} {
		t.Run(where, func(t *testing.T) {
			s, r, f, lease := discoveryTestFixture(t)
			failure := ports.ErrUnavailable
			if where == "record" {
				r.recordErr = failure
			} else {
				r.completeErr = failure
			}
			if err := s.Execute(t.Context(), lease); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if f.released.Load() != 1 || int(f.closed.Load()) != f.opens {
				t.Fatal("writeback failure leaked physical roots")
			}
		})
	}
}
func TestUntrackedDiscoveryCancellationWaitsActualWalk(t *testing.T) {
	s, r, f, lease := discoveryTestFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	f.walk = func(_ context.Context, _ func(string, ports.FileIdentity) error) error {
		close(entered)
		<-resume
		return nil
	}
	go func() { done <- s.Execute(ctx, lease) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("walk did not start")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("physical call detached after cancellation")
	case <-time.After(25 * time.Millisecond):
	}
	if f.released.Load() != 0 {
		t.Fatal("slot released before actual I/O returned")
	}
	close(resume)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("owned execution did not finish")
	}
	if r.completed != 0 || len(r.batches) != 0 || f.released.Load() != 1 || int(f.closed.Load()) != f.opens {
		t.Fatal("late success committed or leaked physical roots")
	}
}
func TestUntrackedDiscoveryEmptyTraversalAndCancelledInput(t *testing.T) {
	s, r, f, lease := discoveryTestFixture(t)
	f.entries = nil
	if err := s.Execute(t.Context(), lease); err != nil || r.completed != 1 || len(r.batches) != 0 {
		t.Fatal("empty coverage synthesized file observations", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := r.loaded
	if err := s.Execute(ctx, lease); !errors.Is(err, context.Canceled) || r.loaded != before {
		t.Fatal("cancelled input entered repository", err)
	}
	var absent *UntrackedDiscoveryService
	if err := absent.Execute(t.Context(), lease); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestUntrackedDiscoveryNonVideoFilesRemainUnpublished(t *testing.T) {
	s, r, f, lease := discoveryTestFixture(t)
	for n, p := range []string{"movie.nfo", "movie.zh-cn.srt", "poster.jpg", "partial.part"} {
		f.entries[p] = ports.FileIdentity{Device: 6, Inode: int64(500 + n), Size: 10, Mtime: 1}
	}
	if err := s.Execute(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if len(r.batches) != 1 || len(r.batches[0]) != 1 || r.batches[0][0].RelativePath != "Movie.mkv" || r.completed != 1 || f.released.Load() != 1 {
		t.Fatal("non-video files reached observations or blocked sweep completion")
	}
}
