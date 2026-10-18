//go:build linux

// Package rootfs confines physical operations to pinned directory capabilities.
package rootfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Root struct {
	fd        int
	closeOnce sync.Once
}

func parts(path string) ([]string, error) {
	if path == "" || filepath.IsAbs(path) {
		return nil, ErrEscape
	}
	var result []string
	for _, p := range strings.Split(path, "/") {
		if p == ".." {
			return nil, ErrEscape
		}
		if p != "." && p != "" {
			result = append(result, p)
		}
	}
	return result, nil
}

// walk pins each directory with O_NOFOLLOW; no absolute path is used after Open.
func walk(fd int, path []string, create bool) (int, error) {
	cur, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, p := range path {
		next, err := unix.Openat(cur, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			err = unix.Mkdirat(cur, p, 0755)
			if err == nil {
				err = unix.Fsync(cur)
			}
			if err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(cur, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		_ = unix.Close(cur)
		if err != nil {
			if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return -1, ErrSymlink
			}
			return -1, err
		}
		cur = next
	}
	return cur, nil
}

func Open(path string) (*Root, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrEscape
	}
	p, err := parts(strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	pinned, err := walk(fd, p, false)
	if err != nil {
		return nil, err
	}
	return &Root{fd: pinned}, nil
}
func (r *Root) Close() { r.closeOnce.Do(func() { _ = unix.Close(r.fd) }) }
func (r *Root) Identity() (Identity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(r.fd, &st); err != nil {
		return Identity{}, err
	}
	return identity(st), nil
}

// LockRoots holds kernel locks, not expiring logical leases. Cancellation only
// interrupts acquisition: the caller retains acquired slots until I/O returns.
func LockRoots(ctx context.Context, paths ...string) (map[string]*Root, func(), error) {
	roots := make(map[string]*Root)
	identities := make(map[[2]uint64]*Root)
	closeAll := func() {
		for _, r := range identities {
			r.Close()
		}
	}
	for _, path := range paths {
		if _, ok := roots[path]; ok {
			continue
		}
		r, err := Open(path)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(r.fd, &st); err != nil {
			r.Close()
			closeAll()
			return nil, nil, err
		}
		key := [2]uint64{uint64(st.Dev), st.Ino}
		if other, ok := identities[key]; ok {
			r.Close()
			roots[path] = other
			continue
		}
		roots[path] = r
		identities[key] = r
	}
	// Lock by stable device/inode rather than path spelling (bind-mount aliases).
	keys := make([][2]uint64, 0, len(identities))
	for k := range identities {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		r := identities[k]
		for {
			if err := ctx.Err(); err != nil {
				closeAll()
				return nil, nil, err
			}
			err := unix.Flock(r.fd, unix.LOCK_EX|unix.LOCK_NB)
			if err == nil {
				break
			}
			if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
				closeAll()
				return nil, nil, err
			}
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	// Close each capability exactly once (aliases share the same descriptor).
	release := func() {
		for _, r := range identities {
			r.Close()
		}
	}
	return roots, release, nil
}

func (r *Root) parent(rel string, create bool) (int, string, error) {
	p, err := parts(rel)
	if err != nil {
		return -1, "", err
	}
	if len(p) == 0 {
		return -1, "", ErrEscape
	}
	fd, err := walk(r.fd, p[:len(p)-1], create)
	return fd, p[len(p)-1], err
}

func (r *Root) source(rel string) (*os.File, Identity, error) {
	fd, name, err := r.parent(rel, false)
	if err != nil {
		return nil, Identity{}, err
	}
	defer func() { _ = unix.Close(fd) }()
	src, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			err = ErrSymlink
		}
		return nil, Identity{}, err
	}
	f := os.NewFile(uintptr(src), name)
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, Identity{}, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, Identity{}, ErrIdentity
	}
	var stat unix.Stat_t
	if err := unix.Fstat(src, &stat); err != nil {
		_ = f.Close()
		return nil, Identity{}, err
	}
	return f, identity(stat), nil
}
func identity(s unix.Stat_t) Identity {
	return Identity{Device: int64(s.Dev), Inode: int64(s.Ino), Size: s.Size, Mtime: s.Mtim.Sec*1e9 + s.Mtim.Nsec}
}
func (r *Root) Inspect(rel string) (Identity, error) {
	f, id, err := r.source(rel)
	if err == nil {
		_ = f.Close()
	}
	return id, err
}

// Hardlink either publishes a new link or observes an existing exact link.
// The source inode is pinned by an FD. /proc/self/fd permits unprivileged linkat
// across two directory capabilities without CAP_DAC_READ_SEARCH (AT_EMPTY_PATH).
// An unavailable procfs fails closed, never falls back to absolute pathname I/O.
func Hardlink(ctx context.Context, src *Root, srcRel string, dst *Root, dstRel string, frozen Identity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, current, err := src.source(srcRel)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if current != frozen {
		return ErrIdentity
	}
	parent, name, err := dst.parent(dstRel, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	err = unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", file.Fd()), parent, name, unix.AT_SYMLINK_FOLLOW)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	var target unix.Stat_t
	if err := unix.Fstatat(parent, name, &target, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if target.Mode&unix.S_IFMT == unix.S_IFLNK {
		return ErrSymlink
	}
	if identity(target) != frozen {
		return ErrConflict
	}
	var after unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &after); err != nil {
		return err
	}
	if identity(after) != frozen {
		return ErrIdentity
	}
	// The original pathname must still reference the pinned source inode.
	if now, err := src.Inspect(srcRel); err != nil || now != frozen {
		return ErrIdentity
	}
	if err := unix.Fsync(parent); err != nil {
		return err
	}
	return ctx.Err()
}
