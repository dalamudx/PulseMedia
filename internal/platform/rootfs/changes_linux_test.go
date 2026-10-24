//go:build linux

package rootfs

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

type watchTestSession struct {
	req    ports.RootWatchRequest
	hints  chan ports.RootChangeHint
	done   chan error
	cancel context.CancelFunc
}

func watchRequest(t *testing.T, path string) ports.RootWatchRequest {
	t.Helper()
	cap, err := NewFilesystem().OpenRoot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cap.Close)
	id, err := cap.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return ports.RootWatchRequest{Root: cap, ConfiguredPath: path, FrozenIdentity: id}
}
func startWatchTest(t *testing.T, path string) *watchTestSession {
	t.Helper()
	s := &watchTestSession{req: watchRequest(t, path), hints: make(chan ports.RootChangeHint, 64), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	s.cancel = cancel
	go func() { s.done <- NewFilesystem().WatchRoot(ctx, s.req, s.hints) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-s.done:
			if !errors.Is(err, context.Canceled) && !errors.Is(err, ports.ErrFileWatchCoverageLost) {
				t.Errorf("watch exit: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("watch did not return")
		}
	})
	s.hint(t, ".", ports.RootChangeRescan)
	return s
}
func (s *watchTestSession) hint(t *testing.T, path string, kind ports.RootChangeKind) {
	t.Helper()
	select {
	case h := <-s.hints:
		if h.RelativePath != path || h.Kind != kind {
			t.Fatalf("hint %+v, want %s %s", h, path, kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hint timeout")
	}
}
func putWatchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRootWatchCloseAndMove(t *testing.T) {
	path := t.TempDir()
	s := startWatchTest(t, path)
	file, err := os.Create(filepath.Join(path, "open.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err = file.WriteString("partial"); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-s.hints:
		t.Fatalf("hint before close: %+v", h)
	case <-time.After(80 * time.Millisecond):
	}
	start := time.Now()
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	s.hint(t, "open.mkv", ports.RootChangeCloseWrite)
	elapsed := time.Since(start)
	t.Logf("close hint latency: %s", elapsed)
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("local event latency %s", elapsed)
	}
	outside := filepath.Join(t.TempDir(), "incoming")
	file, err = os.Create(outside)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err = os.Rename(outside, filepath.Join(path, "moved.mkv")); err != nil {
		t.Fatal(err)
	}
	s.hint(t, "moved.mkv", ports.RootChangeMovedTo)
	// MOVED_TO is observable while the producer still holds its writer open.
	if _, err = file.WriteString("still writing"); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	s.hint(t, "moved.mkv", ports.RootChangeCloseWrite)
}
func TestRootWatchRecursive(t *testing.T) {
	path := t.TempDir()
	if err := os.MkdirAll(filepath.Join(path, "existing", "deep"), 0700); err != nil {
		t.Fatal(err)
	}
	s := startWatchTest(t, path)
	putWatchFile(t, filepath.Join(path, "existing", "deep", "a"))
	s.hint(t, "existing/deep/a", ports.RootChangeCloseWrite)
	if err := os.Mkdir(filepath.Join(path, "new"), 0700); err != nil {
		t.Fatal(err)
	}
	s.hint(t, "new", ports.RootChangeRescan)
	putWatchFile(t, filepath.Join(path, "new", "b"))
	s.hint(t, "new/b", ports.RootChangeCloseWrite)
	incoming := filepath.Join(t.TempDir(), "populated")
	if err := os.MkdirAll(filepath.Join(incoming, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	putWatchFile(t, filepath.Join(incoming, "nested", "old"))
	if err := os.Rename(incoming, filepath.Join(path, "arrived")); err != nil {
		t.Fatal(err)
	}
	s.hint(t, "arrived", ports.RootChangeRescan)
	putWatchFile(t, filepath.Join(path, "arrived", "nested", "c"))
	s.hint(t, "arrived/nested/c", ports.RootChangeCloseWrite)
}
func TestRootWatchSymlinkNoFollow(t *testing.T) {
	path, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	s := startWatchTest(t, path)
	putWatchFile(t, filepath.Join(outside, "outside"))
	if err := os.Symlink(outside, filepath.Join(path, "newlink")); err != nil {
		t.Fatal(err)
	}
	putWatchFile(t, filepath.Join(outside, "again"))
	select {
	case h := <-s.hints:
		t.Fatalf("outside hint %+v", h)
	case <-time.After(100 * time.Millisecond):
	}
	putWatchFile(t, filepath.Join(path, "inside"))
	s.hint(t, "inside", ports.RootChangeCloseWrite)
}
func TestRootWatchRejectsInvalidRequests(t *testing.T) {
	path := t.TempDir()
	valid := watchRequest(t, path)
	typedNil := (*capability)(nil)
	cases := []ports.RootWatchRequest{{}, {Root: typedNil, ConfiguredPath: path}, {Root: &capability{}, ConfiguredPath: path}, valid, valid, valid, valid}
	cases[3].ConfiguredPath = path + "/../bad"
	cases[4].FrozenIdentity.Inode++
	cases[5].ConfiguredPath = t.TempDir()
	cases[6].ConfiguredPath = path + "\\unsafe"
	for i, req := range cases {
		if err := NewFilesystem().WatchRoot(t.Context(), req, make(chan ports.RootChangeHint, 8)); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	if err := NewFilesystem().WatchRoot(t.Context(), valid, nil); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
		t.Fatal(err)
	}
}
func TestRootWatchSwappedRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	s := startWatchTest(t, path)
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	putWatchFile(t, filepath.Join(path+"-old", "stale"))
	select {
	case err := <-s.done:
		if !errors.Is(err, ports.ErrFileWatchCoverageLost) {
			t.Fatal(err)
		}
		s.done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("swap not detected")
	}
	select {
	case h := <-s.hints:
		t.Fatalf("stale hint %+v", h)
	default:
	}
	req := s.req
	if err := NewFilesystem().WatchRoot(t.Context(), req, make(chan ports.RootChangeHint, 8)); !errors.Is(err, ports.ErrFileIdentity) {
		t.Fatal(err)
	}
}
func TestRootWatchDirectoryMoveLosesCoverage(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	s := startWatchTest(t, path)
	if err := os.Rename(filepath.Join(path, "child"), filepath.Join(t.TempDir(), "away")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s.done:
		if !errors.Is(err, ports.ErrFileWatchCoverageLost) {
			t.Fatal(err)
		}
		s.done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("move not detected")
	}
}
func TestRootWatchCancellationAndLocks(t *testing.T) {
	path := t.TempDir()
	s := startWatchTest(t, path)
	lockCtx, cancelLock := context.WithTimeout(t.Context(), time.Second)
	defer cancelLock()
	roots, release, err := NewFilesystem().LockRoots(lockCtx, path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	_ = roots
	start := time.Now()
	s.cancel()
	select {
	case err := <-s.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		s.done <- err
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancellation timeout")
	}
	t.Logf("cancellation latency: %s", time.Since(start))
	if _, err := s.req.Root.Identity(t.Context()); err != nil {
		t.Fatalf("caller root closed: %v", err)
	}
	select {
	case s.hints <- ports.RootChangeHint{}:
	default:
		t.Fatal("caller channel unusable")
	}
}
func TestRootWatchBoundsAndQueue(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	putWatchFile(t, filepath.Join(path, "a"))
	putWatchFile(t, filepath.Join(path, "b"))
	req := watchRequest(t, path)
	for _, limit := range []rootWatchLimits{{1, 64, 100, 4096}, {10, 0, 100, 4096}, {10, 64, 1, 4096}, {10, 64, 100, 1}} {
		if err := NewFilesystem().watchRoot(t.Context(), req, make(chan ports.RootChangeHint, 8), limit); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
			t.Fatal(err)
		}
	}
	out := make(chan ports.RootChangeHint, 1)
	out <- ports.RootChangeHint{}
	if err := NewFilesystem().WatchRoot(t.Context(), req, out); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
		t.Fatal(err)
	}
	s := startWatchTest(t, t.TempDir())
	for i := 0; i < cap(s.hints); i++ {
		s.hints <- ports.RootChangeHint{}
	}
	putWatchFile(t, filepath.Join(s.req.ConfiguredPath, "full"))
	select {
	case err := <-s.done:
		if !errors.Is(err, ports.ErrFileWatchCoverageLost) {
			t.Fatal(err)
		}
		s.done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("full queue ignored")
	}
}
func TestRootWatchDecodeDiscontinuities(t *testing.T) {
	event := func(wd int32, mask uint32, size uint32) []byte {
		b := make([]byte, 16)
		binary.NativeEndian.PutUint32(b[:4], uint32(wd))
		binary.NativeEndian.PutUint32(b[4:8], mask)
		binary.NativeEndian.PutUint32(b[12:16], size)
		return b
	}
	for _, data := range [][]byte{{1}, event(-1, unix.IN_Q_OVERFLOW, 0), event(10, unix.IN_CLOSE_WRITE, 0), event(1, unix.IN_IGNORED, 0), event(1, unix.IN_UNMOUNT, 0), event(1, 0, 0xffffffff), event(1, 0, 4)} {
		w := &rootWatcher{dirs: map[int]watchedDirectory{}}
		if err := w.consume(t.Context(), data); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
			t.Fatalf("decode %x: %v", data, err)
		}
	}
	for _, name := range []string{"..", "a/b", "a\\b", "x\x00", "x\n", string([]byte{255})} {
		if watchNameSafe(name) {
			t.Fatalf("unsafe %q", name)
		}
	}
}

func TestRootWatchReleasesFDs(t *testing.T) {
	path := t.TempDir()
	req := watchRequest(t, path)
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		out := make(chan ports.RootChangeHint, 1)
		out <- ports.RootChangeHint{}
		if err := NewFilesystem().WatchRoot(t.Context(), req, out); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
			t.Fatal(err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("FD growth: before %d after %d", len(before), len(after))
	}
	if _, err := req.Root.Identity(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRootWatchRejectsStaleDirectoryAndMalformedNames(t *testing.T) {
	path := t.TempDir()
	child := filepath.Join(path, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fd, err := unix.Open(child, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "child")
	defer func() { _ = file.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	w := &rootWatcher{root: root, dirs: map[int]watchedDirectory{7: {file: file, rel: "child", id: identity(st)}}}
	event := func(name []byte) []byte {
		b := make([]byte, 16+len(name))
		binary.NativeEndian.PutUint32(b[:4], 7)
		binary.NativeEndian.PutUint32(b[4:8], unix.IN_CLOSE_WRITE)
		binary.NativeEndian.PutUint32(b[12:16], uint32(len(name)))
		copy(b[16:], name)
		return b
	}
	for _, name := range [][]byte{{'a', '/', 'b', 0}, {'a', 0, 'b', 0}, {255, 0, 0, 0}, {'a', 'b', 'c', 'd'}, {'.', '.', 0, 0}} {
		if err := w.consume(t.Context(), event(name)); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
			t.Fatalf("malformed %x: %v", name, err)
		}
	}
	if err := os.Rename(child, child+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := w.consume(t.Context(), event([]byte{'a', 0, 0, 0})); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
		t.Fatalf("stale mapping: %v", err)
	}
}

func TestRootWatchPendingCreateAlreadyEnumerated(t *testing.T) {
	path := t.TempDir()
	if err := os.MkdirAll(filepath.Join(path, "child", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	req := watchRequest(t, path)
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	w := &rootWatcher{request: req, out: make(chan ports.RootChangeHint, 4), limits: rootWatchLimits{3, 2, 2, 4096}, fd: fd, root: req.Root.(*capability).root, dirs: make(map[int]watchedDirectory)}
	defer func() {
		for _, d := range w.dirs {
			_ = d.file.Close()
		}
	}()
	if err := w.register(t.Context(), ".", 0); err != nil {
		t.Fatal(err)
	}
	if len(w.dirs) != 3 || w.entries != 2 {
		t.Fatalf("initial registration: watches=%d entries=%d", len(w.dirs), w.entries)
	}
	owned := make(map[int]watchedDirectory)
	for wd, d := range w.dirs {
		owned[wd] = d
	}
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	// The filesystem is real; replay the precise queued CREATE order deterministically.
	for _, pending := range []struct{ parent, name string }{{".", "child"}, {"child", "nested"}, {".", "child"}} {
		wd := -1
		for candidate, d := range w.dirs {
			if d.rel == pending.parent {
				wd = candidate
				break
			}
		}
		if wd < 0 {
			t.Fatal("missing parent watch")
		}
		size := (len(pending.name) + 1 + 3) &^ 3
		record := make([]byte, 16+size)
		binary.NativeEndian.PutUint32(record[:4], uint32(wd))
		binary.NativeEndian.PutUint32(record[4:8], unix.IN_CREATE|unix.IN_ISDIR)
		binary.NativeEndian.PutUint32(record[12:16], uint32(size))
		copy(record[16:], pending.name)
		if err := w.consume(t.Context(), record); err != nil {
			t.Fatalf("pending CREATE %s: %v", pending.name, err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("duplicate registration FD count: before %d after %d", len(before), len(after))
	}
	if len(w.dirs) != len(owned) || w.entries != 2 {
		t.Fatalf("duplicate consumed limits: watches=%d entries=%d", len(w.dirs), w.entries)
	}
	for wd, d := range owned {
		if w.dirs[wd] != d {
			t.Fatal("duplicate replaced owned watch or FD")
		}
	}
	if err := w.register(t.Context(), "./child", 1); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
		t.Fatalf("distinct alias accepted: %v", err)
	}
	if err := os.Rename(filepath.Join(path, "child"), filepath.Join(path, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := w.register(t.Context(), "child", 1); !errors.Is(err, ports.ErrFileWatchCoverageLost) {
		t.Fatalf("replacement identity accepted: %v", err)
	}
}

func TestRootWatchRestartSameCapability(t *testing.T) {
	path := t.TempDir()
	if err := os.MkdirAll(filepath.Join(path, "existing", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	req := watchRequest(t, path)
	cap := req.Root.(*capability)
	offset, err := unix.Seek(cap.root.fd, 0, unix.SEEK_CUR)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"first", "restart"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			s := &watchTestSession{req: req, hints: make(chan ports.RootChangeHint, 8), done: make(chan error, 1), cancel: cancel}
			go func() { s.done <- NewFilesystem().WatchRoot(ctx, req, s.hints) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-s.done:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("watch return: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("watch failed to stop")
				}
			})
			s.hint(t, ".", ports.RootChangeRescan)
			if phase == "restart" {
				putWatchFile(t, filepath.Join(path, "existing", "nested", "after-restart"))
				s.hint(t, "existing/nested/after-restart", ports.RootChangeCloseWrite)
			}
		})
	}
	after, err := unix.Seek(cap.root.fd, 0, unix.SEEK_CUR)
	if err != nil {
		t.Fatal(err)
	}
	if after != offset {
		t.Fatalf("borrowed directory offset consumed: before %d after %d", offset, after)
	}
	if _, err := req.Root.Identity(t.Context()); err != nil {
		t.Fatalf("borrowed capability closed: %v", err)
	}
}
