//go:build linux

package rootfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/sys/unix"
)

// InspectSidecarDirectory observes a directory identity without creating it.
// The caller must retain the physical root slot; this observation is not a
// configured-namespace or database authorization. PublishSidecar rechecks it.
func (r *Root) InspectSidecarDirectory(ctx context.Context, relative string) (id SidecarDirectoryIdentity, err error) {
	if err = ctx.Err(); err != nil {
		return id, err
	}
	parts, err := sidecarPath(relative, true)
	if err != nil {
		return id, err
	}
	if r == nil {
		return id, ErrIdentity
	}
	fd, err := walk(r.fd, parts, false)
	if err != nil {
		return id, err
	}
	defer func() {
		err = errors.Join(err, unix.Close(fd), ctx.Err())
		if err != nil {
			id = SidecarDirectoryIdentity{}
		}
	}()
	st, err := sidecarStat(fd)
	if err != nil {
		return id, err
	}
	return sidecarDirID(st), ctx.Err()
}

// PublishSidecar publishes bounded bytes without replacing any final entry.
// The caller MUST own the existing LockRoots physical slot until this synchronous
// call and its cleanup return, including after cancellation. It must externally
// revalidate the configured absolute namespace, root identity/marker and current
// DB owner/token/state/lease before and after I/O and before accepting the result.
// A pinned Root cannot prove it still occupies the configured absolute path.
// This primitive neither acquires/releases that slot nor authorizes DB success.
func (r *Root) PublishSidecar(ctx context.Context, req SidecarRequest) (SidecarResult, error) {
	return r.publishSidecar(ctx, req, sidecarOps{})
}

// Per-call seams keep fault tests deterministic without global mutable hooks.
type sidecarOps struct {
	subtitle   bool
	write      func(int, []byte) (int, error)
	sync       func(int) error
	checkpoint func(string)
}

func sidecarPath(path string, directory bool) ([]string, error) {
	if directory && path == "." {
		return nil, nil
	}
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00") {
		return nil, ErrEscape
	}
	p := strings.Split(path, "/")
	for _, part := range p {
		if part == "" || part == "." || part == ".." {
			return nil, ErrEscape
		}
	}
	return p, nil
}

func sidecarDirID(st unix.Stat_t) SidecarDirectoryIdentity {
	return SidecarDirectoryIdentity{Device: int64(st.Dev), Inode: int64(st.Ino)}
}

func sidecarStat(fd int) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := unix.Fstat(fd, &st)
	return st, err
}

func sidecarEntry(dir int, name string) (SidecarResult, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return SidecarResult{Disposition: SidecarNotPublished}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return SidecarResult{Disposition: SidecarUnsafeExisting}, ErrConflict
	}
	return SidecarResult{Disposition: SidecarPreservedRegular, Identity: identity(st)}, nil
}

func (r *Root) publishSidecar(ctx context.Context, req SidecarRequest, ops sidecarOps) (out SidecarResult, err error) {
	out.Disposition = SidecarNotPublished
	defer func() {
		err = errors.Join(err, ctx.Err())
		if out.Published && err != nil {
			out.Disposition = SidecarUncertain
		}
	}()
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if ops.subtitle {
		if !validSubtitleRequest(req) {
			return out, ErrSidecarRequest
		}
	} else if req.Name != "movie.nfo" && req.Name != "tvshow.nfo" && req.Name != "poster.jpg" {
		return out, ErrSidecarRequest
	}
	if len(req.Content) == 0 || len(req.Content) > MaxSidecarBytes {
		return out, ErrSidecarRequest
	}
	source, err := sidecarPath(req.SourceRelative, false)
	if err != nil {
		return out, err
	}
	destination, err := sidecarPath(req.DestinationDirectory, true)
	if err != nil {
		return out, err
	}
	if len(destination) >= len(source) {
		return out, ErrEscape
	}
	for i, p := range destination {
		if source[i] != p {
			return out, ErrEscape
		}
	}
	if r == nil {
		return out, ErrIdentity
	}
	if ops.write == nil {
		ops.write = unix.Write
	}
	if ops.sync == nil {
		ops.sync = unix.Fsync
	}
	checkpoint := func(point string) {
		if ops.checkpoint != nil {
			ops.checkpoint(point)
		}
	}

	// Retain every parent FD, so replacing any intermediate directory is detected.
	root, err := walk(r.fd, nil, false)
	if err != nil {
		return out, err
	}
	dirs := []int{root}
	defer func() {
		for _, fd := range dirs {
			err = errors.Join(err, unix.Close(fd))
		}
	}()
	for _, part := range source[:len(source)-1] {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		next, e := walk(dirs[len(dirs)-1], []string{part}, false)
		if e != nil {
			return out, e
		}
		dirs = append(dirs, next)
	}
	dest := dirs[len(destination)]
	st, err := sidecarStat(dest)
	if err != nil {
		return out, err
	}
	if sidecarDirID(st) != req.DestinationIdentity {
		return out, ErrIdentity
	}
	parent, leaf := dirs[len(dirs)-1], source[len(source)-1]
	if err = ctx.Err(); err != nil {
		return out, err
	}
	input, err := unix.Openat(parent, leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, unix.Close(input)) }()
	validate := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		for i := 1; i < len(dirs); i++ {
			var entry unix.Stat_t
			if e := unix.Fstatat(dirs[i-1], source[i-1], &entry, unix.AT_SYMLINK_NOFOLLOW); e != nil {
				return e
			}
			pinned, e := sidecarStat(dirs[i])
			if e != nil {
				return e
			}
			if entry.Mode&unix.S_IFMT != unix.S_IFDIR || sidecarDirID(entry) != sidecarDirID(pinned) {
				return ErrIdentity
			}
			if e = ctx.Err(); e != nil {
				return e
			}
		}
		pinned, e := sidecarStat(input)
		if e != nil {
			return e
		}
		var entry unix.Stat_t
		if e = unix.Fstatat(parent, leaf, &entry, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			return e
		}
		if pinned.Mode&unix.S_IFMT != unix.S_IFREG || entry.Mode&unix.S_IFMT != unix.S_IFREG || identity(pinned) != req.SourceIdentity || identity(entry) != req.SourceIdentity {
			return ErrIdentity
		}
		return ctx.Err()
	}
	if err = validate(); err != nil {
		return out, err
	}
	existing, e := sidecarEntry(dest, req.Name)
	if !errors.Is(e, unix.ENOENT) {
		if e != nil && existing.Disposition != SidecarUnsafeExisting {
			return out, e
		}
		return existing, errors.Join(e, validate())
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return out, err
	}
	stage := ".pulsemedia-sidecar-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(dest, stage, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return out, err
	}
	// Only this fresh inode is ever written or eligible for temporary cleanup.
	staged, statErr := sidecarStat(fd)
	defer func() {
		var current unix.Stat_t
		e := unix.Fstatat(dest, stage, &current, unix.AT_SYMLINK_NOFOLLOW)
		if e == nil {
			if statErr != nil || current.Mode&unix.S_IFMT != unix.S_IFREG || sidecarDirID(current) != sidecarDirID(staged) {
				e = ErrIdentity
			} else {
				e = unix.Unlinkat(dest, stage, 0)
			}
		} else if errors.Is(e, unix.ENOENT) {
			e = nil
		}
		// Cleanup still runs after cancellation; it never releases the caller's slot.
		err = errors.Join(err, e, unix.Fsync(dest), unix.Close(fd))
		if out.Published && err != nil {
			out.Disposition = SidecarUncertain
		}
	}()
	if statErr != nil {
		return out, statErr
	}
	checkpoint("staged")
	for offset := 0; offset < len(req.Content); {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		end := min(offset+64*1024, len(req.Content))
		n, e := ops.write(fd, req.Content[offset:end])
		if n < 0 || n > end-offset {
			return out, io.ErrShortWrite
		}
		offset += n
		if e != nil {
			return out, e
		}
		if n == 0 {
			return out, io.ErrShortWrite
		}
		if err = ctx.Err(); err != nil {
			return out, err
		}
	}
	// Staging remains private until complete. Finished metadata is readable by
	// other media-server UIDs, independently of this process's private umask.
	if err = unix.Fchmod(fd, 0644); err != nil {
		return out, err
	}
	if err = ops.sync(fd); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	staged, err = sidecarStat(fd)
	if err != nil {
		return out, err
	}
	if staged.Size != int64(len(req.Content)) {
		return out, io.ErrShortWrite
	}
	checkpoint("before_publish")
	if err = validate(); err != nil {
		return out, err
	}
	// Link the pinned FD, never the replaceable staging pathname. linkat is atomic
	// no-replace even for symlinks/directories; unavailable procfs fails closed.
	err = unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", fd), dest, req.Name, unix.AT_SYMLINK_FOLLOW)
	if errors.Is(err, unix.EEXIST) {
		existing, e = sidecarEntry(dest, req.Name)
		return existing, errors.Join(e, validate())
	}
	if err != nil {
		return out, err
	}
	out = SidecarResult{Disposition: SidecarUncertain, Published: true, Identity: identity(staged)}
	checkpoint("published")
	// Once published, errors/cancellation retain the actual result and final entry.
	if err = ops.sync(dest); err != nil {
		return out, err
	}
	if err = validate(); err != nil {
		return out, err
	}
	var final unix.Stat_t
	if err = unix.Fstatat(dest, req.Name, &final, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return out, err
	}
	if final.Mode&unix.S_IFMT != unix.S_IFREG || identity(final) != identity(staged) {
		return out, ErrIdentity
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	out.Disposition = SidecarCreated
	return out, nil
}
