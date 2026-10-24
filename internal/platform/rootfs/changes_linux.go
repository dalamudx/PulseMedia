//go:build linux

package rootfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

var _ ports.RootChangeFilesystem = (*Filesystem)(nil)

type rootWatchLimits struct{ watches, depth, entries, buffer int }
type watchedDirectory struct {
	file *os.File
	rel  string
	id   Identity
}
type rootWatcher struct {
	request ports.RootWatchRequest
	out     chan<- ports.RootChangeHint
	limits  rootWatchLimits
	fd      int
	root    *Root
	dirs    map[int]watchedDirectory
	entries int
}

func (f *Filesystem) WatchRoot(ctx context.Context, req ports.RootWatchRequest, out chan<- ports.RootChangeHint) error {
	return f.watchRoot(ctx, req, out, rootWatchLimits{4096, 64, 1000000, 64 * 1024})
}

func (f *Filesystem) watchRoot(ctx context.Context, req ports.RootWatchRequest, out chan<- ports.RootChangeHint, limits rootWatchLimits) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cap, ok := req.Root.(*capability)
	if !ok || cap == nil || cap.root == nil {
		return ports.ErrFileIdentity
	}
	if !filepath.IsAbs(req.ConfiguredPath) || filepath.Clean(req.ConfiguredPath) != req.ConfiguredPath || !watchPathSafe(req.ConfiguredPath) {
		return ports.ErrFileEscape
	}
	if out == nil {
		return ports.ErrFileWatchCoverageLost
	}
	if limits.watches < 1 || limits.depth < 0 || limits.entries < 1 || limits.buffer < 4096 || limits.buffer > 1024*1024 {
		return ports.ErrFileWatchCoverageLost
	}
	pinned, err := unix.FcntlInt(uintptr(cap.root.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return ports.ErrFileIdentity
	}
	root := &Root{fd: pinned}
	defer root.Close()
	w := &rootWatcher{request: req, out: out, limits: limits, root: root, dirs: make(map[int]watchedDirectory)}
	if err := w.namespace(ctx); err != nil {
		return err
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(pinned, &fs); err != nil {
		return ports.ErrFileWatchUnavailable
	}
	// These remote filesystems cannot promise delivery for remote writers.
	switch uint64(fs.Type) {
	case 0x6969, 0x517B, 0xFF534D42, 0x73757245, 0x01021997:
		return ports.ErrFileWatchUnavailable
	}
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrFileWatchUnavailable, err)
	}
	w.fd = fd
	defer func() {
		_ = unix.Close(fd)
		for _, d := range w.dirs {
			_ = d.file.Close()
		}
	}()
	if err := w.register(ctx, ".", 0); err != nil {
		return err
	}
	if err := w.emit(ctx, ".", ports.RootChangeRescan); err != nil {
		return err
	}
	buffer := make([]byte, limits.buffer)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, 50); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return ports.ErrFileWatchCoverageLost
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return ports.ErrFileWatchCoverageLost
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(fd, buffer)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n == 0 {
			return ports.ErrFileWatchCoverageLost
		}
		if err := w.consume(ctx, buffer[:n]); err != nil {
			return err
		}
	}
}

func watchNameSafe(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func watchPathSafe(path string) bool {
	if path == "/" {
		return true
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if !watchNameSafe(part) {
			return false
		}
	}
	return true
}
func sameWatchIdentity(a, b Identity) bool { return a.Device == b.Device && a.Inode == b.Inode }
func (w *rootWatcher) namespace(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := w.root.Identity()
	if err != nil || !sameWatchIdentity(id, w.request.FrozenIdentity) {
		return ports.ErrFileIdentity
	}
	current, err := Open(w.request.ConfiguredPath)
	if err != nil {
		return ports.ErrFileIdentity
	}
	defer current.Close()
	actual, err := current.Identity()
	if err != nil || !sameWatchIdentity(actual, id) {
		return ports.ErrFileIdentity
	}
	return nil
}
func (w *rootWatcher) directory(ctx context.Context, d watchedDirectory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := parts(d.rel)
	if err != nil {
		return ports.ErrFileWatchCoverageLost
	}
	fd, err := walk(w.root.fd, p, false)
	if err != nil {
		return ports.ErrFileWatchCoverageLost
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !sameWatchIdentity(identity(st), d.id) {
		return ports.ErrFileWatchCoverageLost
	}
	return nil
}
func (w *rootWatcher) emit(ctx context.Context, rel string, kind ports.RootChangeKind) error {
	directoryPath := filepath.Dir(rel)
	if kind == ports.RootChangeRescan {
		directoryPath = rel
	}
	found := false
	for _, d := range w.dirs {
		if d.rel == directoryPath {
			found = true
			if err := w.directory(ctx, d); err != nil {
				return err
			}
			break
		}
	}
	if !found {
		return ports.ErrFileWatchCoverageLost
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.namespace(ctx); err != nil {
		return fmt.Errorf("%w: %v", ports.ErrFileWatchCoverageLost, err)
	}
	select {
	case w.out <- ports.RootChangeHint{RelativePath: rel, Kind: kind}:
		return nil
	default:
		return ports.ErrFileWatchCoverageLost
	}
}
func (w *rootWatcher) register(ctx context.Context, rel string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.namespace(ctx); err != nil {
		return fmt.Errorf("%w: %v", ports.ErrFileWatchCoverageLost, err)
	}
	p, err := parts(rel)
	if err != nil {
		return ports.ErrFileWatchCoverageLost
	}
	var fd int
	if len(p) == 0 {
		// A dup shares directory offsets; enumeration needs its own open description.
		fd, err = unix.Openat(w.root.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	} else {
		fd, err = walk(w.root.fd, p, false)
	}
	if err != nil {
		return ports.ErrFileWatchCoverageLost
	}
	file := os.NewFile(uintptr(fd), rel)
	owned := false
	defer func() {
		if !owned {
			_ = file.Close()
		}
	}()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return ports.ErrFileWatchCoverageLost
	}
	// Enumeration may register a child before its queued parent CREATE arrives.
	for _, existing := range w.dirs {
		sameIdentity := sameWatchIdentity(existing.id, identity(st))
		if existing.rel == rel || sameIdentity {
			if existing.rel != rel || !sameIdentity {
				return ports.ErrFileWatchCoverageLost
			}
			if err := w.directory(ctx, existing); err != nil {
				return err
			}
			if err := w.namespace(ctx); err != nil {
				return fmt.Errorf("%w: %v", ports.ErrFileWatchCoverageLost, err)
			}
			return nil
		}
	}
	if len(w.dirs) >= w.limits.watches || depth > w.limits.depth {
		return ports.ErrFileWatchCoverageLost
	}
	// Follow only the procfs link to the pinned FD, never a configured pathname.
	wd, err := unix.InotifyAddWatch(w.fd, fmt.Sprintf("/proc/self/fd/%d", fd), unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO|unix.IN_CREATE|unix.IN_MOVED_FROM|unix.IN_DELETE|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_ONLYDIR)
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrFileWatchCoverageLost, err)
	}
	if _, exists := w.dirs[wd]; exists {
		return ports.ErrFileWatchCoverageLost
	}
	d := watchedDirectory{file: file, rel: rel, id: identity(st)}
	w.dirs[wd] = d
	owned = true
	if err := w.directory(ctx, d); err != nil {
		return err
	}
	// Parent registration precedes enumeration, closing the child discovery gap.
	for {
		entries, err := file.ReadDir(64)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			w.entries++
			if w.entries > w.limits.entries || !watchNameSafe(entry.Name()) {
				return ports.ErrFileWatchCoverageLost
			}
			var child unix.Stat_t
			if unix.Fstatat(fd, entry.Name(), &child, unix.AT_SYMLINK_NOFOLLOW) != nil {
				return ports.ErrFileWatchCoverageLost
			}
			if child.Mode&unix.S_IFMT == unix.S_IFDIR {
				if err := w.register(ctx, filepath.Join(rel, entry.Name()), depth+1); err != nil {
					return err
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ports.ErrFileWatchCoverageLost
		}
	}
}

func (w *rootWatcher) consume(ctx context.Context, data []byte) error {
	for len(data) > 0 {
		if len(data) < unix.SizeofInotifyEvent {
			return ports.ErrFileWatchCoverageLost
		}
		wd := int(int32(binary.NativeEndian.Uint32(data[:4])))
		mask := binary.NativeEndian.Uint32(data[4:8])
		size := uint64(binary.NativeEndian.Uint32(data[12:16]))
		if size > 256 || size%4 != 0 || size > uint64(len(data)-unix.SizeofInotifyEvent) {
			return ports.ErrFileWatchCoverageLost
		}
		raw := data[16 : 16+int(size)]
		data = data[16+int(size):]
		if mask&(unix.IN_Q_OVERFLOW|unix.IN_IGNORED|unix.IN_UNMOUNT|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
			return ports.ErrFileWatchCoverageLost
		}
		d, ok := w.dirs[wd]
		if !ok {
			return ports.ErrFileWatchCoverageLost
		}
		if err := w.directory(ctx, d); err != nil {
			return err
		}
		if size == 0 {
			return ports.ErrFileWatchCoverageLost
		}
		end := bytes.IndexByte(raw, 0)
		if end < 0 {
			return ports.ErrFileWatchCoverageLost
		}
		for _, b := range raw[end:] {
			if b != 0 {
				return ports.ErrFileWatchCoverageLost
			}
		}
		name := string(raw[:end])
		if !watchNameSafe(name) {
			return ports.ErrFileWatchCoverageLost
		}
		rel := filepath.Join(d.rel, name)
		if mask&unix.IN_ISDIR != 0 {
			if mask&(unix.IN_MOVED_FROM|unix.IN_DELETE) != 0 {
				return ports.ErrFileWatchCoverageLost
			}
			if mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
				if err := w.register(ctx, rel, strings.Count(rel, "/")+1); err != nil {
					return err
				}
				if err := w.emit(ctx, rel, ports.RootChangeRescan); err != nil {
					return err
				}
			}
			continue
		}
		kind := ports.RootChangeKind("")
		if mask&unix.IN_CLOSE_WRITE != 0 {
			kind = ports.RootChangeCloseWrite
		} else if mask&unix.IN_MOVED_TO != 0 {
			kind = ports.RootChangeMovedTo
		}
		if kind == "" {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(int(d.file.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return ports.ErrFileWatchCoverageLost
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		if err := w.directory(ctx, d); err != nil {
			return err
		}
		if err := w.emit(ctx, rel, kind); err != nil {
			return err
		}
	}
	return nil
}
