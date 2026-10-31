//go:build linux

package rootfs

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

var _ ports.LibraryMaintenanceFilesystem = (*Filesystem)(nil)

func (*Filesystem) MaintenanceInspect(ctx context.Context, root ports.TransferRoot, rel string) (ports.MaintenanceFileObservation, error) {
	c, ok := root.(*capability)
	if !ok {
		return ports.MaintenanceFileObservation{}, ErrIdentity
	}
	if e := ctx.Err(); e != nil {
		return ports.MaintenanceFileObservation{}, e
	}
	f, id, e := c.root.source(rel)
	if e != nil {
		return ports.MaintenanceFileObservation{}, e
	}
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if e = unix.Fstat(int(f.Fd()), &st); e != nil {
		return ports.MaintenanceFileObservation{}, e
	}
	return ports.MaintenanceFileObservation{Identity: id, AllocatedBytes: st.Blocks * 512, LinkCount: int64(st.Nlink)}, nil
}

// MaintenanceRename is separate from Recycle: it never grants the ability to
// retire another file or overwrite a path, and does not change recycle semantics.
func (*Filesystem) MaintenanceRename(ctx context.Context, root ports.TransferRoot, source, target string, frozen ports.FileIdentity) error {
	c, ok := root.(*capability)
	if !ok {
		return ErrIdentity
	}
	sp, se := parts(source)
	tp, te := parts(target)
	if se != nil || te != nil || len(sp) == 0 || len(tp) == 0 || source != strings.Join(sp, "/") || target != strings.Join(tp, "/") || strings.HasPrefix(tp[0], ".pulsemedia") || source == target {
		return ErrEscape
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	f, id, e := c.root.source(source)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	if id != frozen {
		return ErrIdentity
	}
	sfd, sname, e := c.root.parent(source, false)
	if e != nil {
		return e
	}
	defer func() { _ = unix.Close(sfd) }()
	tfd, tname, e := c.root.parent(target, true)
	if e != nil {
		return e
	}
	defer func() { _ = unix.Close(tfd) }()
	if now, e := c.root.Inspect(source); e != nil || now != frozen {
		return ErrIdentity
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = unix.Renameat2(sfd, sname, tfd, tname, unix.RENAME_NOREPLACE); e != nil {
		if errors.Is(e, unix.EEXIST) {
			return ErrConflict
		}
		return e
	}
	if now, e := c.root.Inspect(target); e != nil || now != frozen {
		return ErrIdentity
	}
	var st unix.Stat_t
	if e = unix.Fstat(int(f.Fd()), &st); e != nil || identity(st) != frozen {
		return ErrIdentity
	}
	if e = unix.Fstatat(sfd, sname, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
		return ErrConflict
	}
	if e = unix.Fsync(tfd); e != nil {
		return e
	}
	if e = unix.Fsync(sfd); e != nil {
		return e
	}
	return ctx.Err()
}

func (*Filesystem) MaintenanceRemove(ctx context.Context, root ports.TransferRoot, rel string, frozen ports.FileIdentity) error {
	c, ok := root.(*capability)
	if !ok {
		return ErrIdentity
	}
	p, e := parts(rel)
	if e != nil || len(p) < 3 || p[0] != ".pulsemedia_recycle" || rel != strings.Join(p, "/") {
		return ErrEscape
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	f, id, e := c.root.source(rel)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	if id != frozen {
		return ErrIdentity
	}
	sfd, name, e := c.root.parent(rel, false)
	if e != nil {
		return e
	}
	defer func() { _ = unix.Close(sfd) }()
	// First isolate the candidate in a private, exclusively created directory.
	// A raced source is preserved there and reported unknown, never unlinked.
	stage := ".pulsemedia-cleanup-" + strconv.FormatInt(frozen.Inode, 10) + "-" + strconv.FormatInt(frozen.Mtime, 10)
	if e = unix.Mkdirat(sfd, stage, 0700); e != nil {
		return e
	}
	qfd, e := unix.Openat(sfd, stage, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	defer func() { _ = unix.Close(qfd) }()
	if now, e := c.root.Inspect(rel); e != nil || now != frozen {
		_ = unix.Unlinkat(sfd, stage, unix.AT_REMOVEDIR)
		return ErrIdentity
	}
	if e = ctx.Err(); e != nil {
		_ = unix.Unlinkat(sfd, stage, unix.AT_REMOVEDIR)
		return e
	}
	if e = unix.Renameat2(sfd, name, qfd, "file", unix.RENAME_NOREPLACE); e != nil {
		_ = unix.Unlinkat(sfd, stage, unix.AT_REMOVEDIR)
		return e
	}
	var st unix.Stat_t
	if e = unix.Fstatat(qfd, "file", &st, unix.AT_SYMLINK_NOFOLLOW); e != nil || identity(st) != frozen || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrIdentity
	}
	if e = unix.Fstat(int(f.Fd()), &st); e != nil || identity(st) != frozen {
		return ErrIdentity
	}
	if e = unix.Fsync(qfd); e != nil {
		return e
	}
	if e = unix.Fsync(sfd); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = unix.Unlinkat(qfd, "file", 0); e != nil {
		return e
	}
	if e = unix.Fsync(qfd); e != nil {
		return e
	}
	if e = unix.Unlinkat(sfd, stage, unix.AT_REMOVEDIR); e != nil {
		return e
	}
	if e = unix.Fsync(sfd); e != nil {
		return e
	}
	return ctx.Err()
}
