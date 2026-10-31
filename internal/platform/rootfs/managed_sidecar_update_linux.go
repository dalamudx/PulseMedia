//go:build linux

package rootfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

var _ ports.ManagedSidecarUpdateFilesystem = (*Filesystem)(nil)

// The caller owns the physical root slot through this call and durable writeback.
func (*Filesystem) ReadManagedSidecar(ctx context.Context, root ports.TransferRoot, req ports.SidecarRequest) (out ports.ManagedSidecarContent, err error) {
	c, ok := root.(*capability)
	if !ok || c == nil || c.root == nil {
		return out, sidecarPortError(errors.Join(ctx.Err(), ErrIdentity))
	}
	view, err := openManagedSidecar(ctx, c.root, req)
	if err != nil {
		return out, sidecarPortError(err)
	}
	defer func() {
		err = sidecarPortError(errors.Join(err, view.close(), ctx.Err()))
		if err != nil {
			out = ports.ManagedSidecarContent{}
		}
	}()
	out, err = view.read(ctx, req.Name)
	if err == nil {
		err = view.validate(ctx)
	}
	return out, err
}

func (*Filesystem) ReplaceManagedSidecar(ctx context.Context, root ports.TransferRoot, req ports.SidecarRequest, old ports.ManagedSidecarContent, permit uuid.UUID) (ports.SidecarResult, error) {
	c, ok := root.(*capability)
	if !ok || c == nil || c.root == nil {
		return ports.SidecarResult{Disposition: SidecarNotPublished}, sidecarPortError(errors.Join(ctx.Err(), ErrIdentity))
	}
	out, err := c.root.replaceManagedSidecar(ctx, req, old, permit, managedSidecarOps{})
	return out, sidecarPortError(err)
}

// Hooks are local to one synchronous call; tests never alter process-wide state.
type managedSidecarOps struct {
	checkpoint func(string)
	write      func(int, []byte) (int, error)
	sync       func(int) error
	rename     func(int, string, int, string, uint) error
}

type managedSidecarView struct {
	dirs      []int
	source    int
	req       SidecarRequest
	parts     []string
	namespace string
	rootID    SidecarDirectoryIdentity
}

func openManagedSidecar(ctx context.Context, r *Root, req SidecarRequest) (_ *managedSidecarView, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if req.Name != "movie.nfo" && req.Name != "tvshow.nfo" && req.Name != "poster.jpg" {
		return nil, ErrSidecarRequest
	}
	source, err := sidecarPath(req.SourceRelative, false)
	if err != nil {
		return nil, err
	}
	dest, err := sidecarPath(req.DestinationDirectory, true)
	if err != nil {
		return nil, err
	}
	if len(dest) >= len(source) {
		return nil, ErrEscape
	}
	for i := range dest {
		if dest[i] != source[i] {
			return nil, ErrEscape
		}
	}
	if r == nil {
		return nil, ErrIdentity
	}
	v := &managedSidecarView{source: -1, req: req, parts: source}
	defer func() {
		if err != nil {
			err = errors.Join(err, v.close())
		}
	}()
	root, err := walk(r.fd, nil, false)
	if err != nil {
		return nil, err
	}
	v.dirs = append(v.dirs, root)
	st, err := sidecarStat(root)
	if err != nil {
		return nil, err
	}
	v.rootID = sidecarDirID(st)
	// Root has no configured pathname. Pin its current namespace and fail closed if
	// it moves during I/O; the caller separately checks the configured binding.
	path := make([]byte, 4096)
	n, err := unix.Readlink(fmt.Sprintf("/proc/self/fd/%d", root), path)
	if err != nil {
		return nil, err
	}
	if n == len(path) || n == 0 || strings.HasSuffix(string(path[:n]), " (deleted)") {
		return nil, ErrIdentity
	}
	v.namespace = string(path[:n])
	for _, part := range source[:len(source)-1] {
		fd, e := walk(v.dirs[len(v.dirs)-1], []string{part}, false)
		if e != nil {
			return nil, e
		}
		v.dirs = append(v.dirs, fd)
	}
	v.source, err = unix.Openat(v.dirs[len(v.dirs)-1], source[len(source)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err = v.validate(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *managedSidecarView) close() error {
	var err error
	if v.source >= 0 {
		err = unix.Close(v.source)
	}
	for _, fd := range v.dirs {
		err = errors.Join(err, unix.Close(fd))
	}
	return err
}

func (v *managedSidecarView) dest() int {
	if v.req.DestinationDirectory == "." {
		return v.dirs[0]
	}
	return v.dirs[len(strings.Split(v.req.DestinationDirectory, "/"))]
}

func (v *managedSidecarView) validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := Open(v.namespace)
	if err != nil {
		return err
	}
	st, e := sidecarStat(root.fd)
	root.Close()
	if e != nil {
		return e
	}
	pinnedRoot, err := sidecarStat(v.dirs[0])
	if err != nil {
		return err
	}
	if sidecarDirID(st) != v.rootID || sidecarDirID(pinnedRoot) != v.rootID {
		return ErrIdentity
	}
	for i := 1; i < len(v.dirs); i++ {
		var entry unix.Stat_t
		if err := unix.Fstatat(v.dirs[i-1], v.parts[i-1], &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		pinned, err := sidecarStat(v.dirs[i])
		if err != nil {
			return err
		}
		if entry.Mode&unix.S_IFMT != unix.S_IFDIR || sidecarDirID(entry) != sidecarDirID(pinned) {
			return ErrIdentity
		}
	}
	dest, err := sidecarStat(v.dest())
	if err != nil {
		return err
	}
	if sidecarDirID(dest) != v.req.DestinationIdentity {
		return ErrIdentity
	}
	source, err := sidecarStat(v.source)
	if err != nil {
		return err
	}
	var entry unix.Stat_t
	if err := unix.Fstatat(v.dirs[len(v.dirs)-1], v.parts[len(v.parts)-1], &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if source.Mode&unix.S_IFMT != unix.S_IFREG || entry.Mode&unix.S_IFMT != unix.S_IFREG || identity(source) != v.req.SourceIdentity || identity(entry) != v.req.SourceIdentity {
		return ErrIdentity
	}
	return ctx.Err()
}

func managedHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (v *managedSidecarView) read(ctx context.Context, name string) (out ports.ManagedSidecarContent, err error) {
	if err = v.validate(ctx); err != nil {
		return out, err
	}
	fd, err := unix.Openat(v.dest(), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, unix.Close(fd)) }()
	before, err := sidecarStat(fd)
	if err != nil {
		return out, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || sidecarDirID(before) == (SidecarDirectoryIdentity{Device: v.req.SourceIdentity.Device, Inode: v.req.SourceIdentity.Inode}) {
		return out, ErrConflict
	}
	if before.Size <= 0 || before.Size > MaxSidecarBytes {
		return out, ErrSidecarRequest
	}
	content := make([]byte, int(before.Size)+1)
	offset := 0
	for offset < len(content) {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		n, e := unix.Read(fd, content[offset:min(offset+64*1024, len(content))])
		if e != nil {
			return out, e
		}
		if n == 0 {
			break
		}
		offset += n
	}
	after, err := sidecarStat(fd)
	if err != nil {
		return out, err
	}
	var entry unix.Stat_t
	if err = unix.Fstatat(v.dest(), name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return out, err
	}
	if offset != int(before.Size) || identity(before) != identity(after) || identity(entry) != identity(before) || after.Ctim != before.Ctim || after.Nlink != 1 || entry.Nlink != 1 || entry.Mode&unix.S_IFMT != unix.S_IFREG {
		return out, ErrIdentity
	}
	if err = v.validate(ctx); err != nil {
		return out, err
	}
	content = content[:offset]
	return ports.ManagedSidecarContent{Name: v.req.Name, Directory: v.req.DestinationDirectory, Identity: identity(after), Content: content, Hash: managedHash(content)}, nil
}

func sameManagedSidecar(a, b ports.ManagedSidecarContent) bool {
	return a.Name == b.Name && a.Directory == b.Directory && a.Identity == b.Identity && a.Hash == b.Hash && bytes.Equal(a.Content, b.Content)
}

func (r *Root) replaceManagedSidecar(ctx context.Context, req SidecarRequest, old ports.ManagedSidecarContent, permit uuid.UUID, ops managedSidecarOps) (out SidecarResult, err error) {
	out.Disposition = SidecarNotPublished
	touched := false
	defer func() {
		err = errors.Join(err, ctx.Err())
		if err != nil && touched {
			out.Disposition = SidecarUncertain
		}
	}()
	if permit == uuid.Nil || len(req.Content) == 0 || len(req.Content) > MaxSidecarBytes || old.Name != req.Name || old.Directory != req.DestinationDirectory || len(old.Content) == 0 || len(old.Content) > MaxSidecarBytes || old.Hash != managedHash(old.Content) {
		return out, ErrSidecarRequest
	}
	v, err := openManagedSidecar(ctx, r, req)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, v.close()) }()
	check := func(name string) error {
		current, e := v.read(ctx, name)
		if e != nil {
			return e
		}
		if !sameManagedSidecar(current, old) {
			return ErrIdentity
		}
		return nil
	}
	if err = check(req.Name); err != nil {
		return out, err
	}
	if ops.write == nil {
		ops.write = unix.Write
	}
	if ops.sync == nil {
		ops.sync = unix.Fsync
	}
	if ops.rename == nil {
		ops.rename = unix.Renameat2
	}
	point := func(s string) {
		if ops.checkpoint != nil {
			ops.checkpoint(s)
		}
	}
	dest := v.dest()
	backup := ".pulsemedia-update-" + permit.String() + "-" + req.Name + ".bak"
	stage := ".pulsemedia-update-" + permit.String() + "-" + req.Name + ".stage"
	// An existing permit artifact forbids another execution, even if the target
	// happens to match. Recovery observes these artifacts without replaying writes.
	var entry unix.Stat_t
	if e := unix.Fstatat(dest, backup, &entry, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
		return out, errors.Join(ErrConflict, e)
	}
	fd, err := unix.Openat(dest, stage, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return out, err
	}
	staged, statErr := sidecarStat(fd)
	preserve := false
	defer func() {
		if !preserve {
			var current unix.Stat_t
			e := unix.Fstatat(dest, stage, &current, unix.AT_SYMLINK_NOFOLLOW)
			if e == nil {
				if statErr != nil || sidecarDirID(current) != sidecarDirID(staged) || current.Mode&unix.S_IFMT != unix.S_IFREG {
					e = ErrIdentity
				} else {
					e = unix.Unlinkat(dest, stage, 0)
				}
			} else if errors.Is(e, unix.ENOENT) {
				e = nil
			}
			err = errors.Join(err, e)
		}
		err = errors.Join(err, ops.sync(dest), unix.Close(fd))
	}()
	if statErr != nil {
		return out, statErr
	}
	point("staged")
	for offset := 0; offset < len(req.Content); {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		end := min(offset+64*1024, len(req.Content))
		n, e := ops.write(fd, req.Content[offset:end])
		if n < 0 || n > end-offset || n == 0 {
			return out, errors.Join(io.ErrShortWrite, e)
		}
		offset += n
		if e != nil {
			return out, e
		}
	}
	if err = unix.Fchmod(fd, 0644); err != nil {
		return out, err
	}
	if err = ops.sync(fd); err != nil {
		return out, err
	}
	staged, err = sidecarStat(fd)
	if err != nil {
		return out, err
	}
	verifyStage := func(name string) error {
		content, e := v.read(ctx, name)
		if e != nil {
			return e
		}
		if content.Identity != identity(staged) || content.Hash != managedHash(req.Content) || !bytes.Equal(content.Content, req.Content) {
			return ErrIdentity
		}
		return nil
	}
	if err = verifyStage(stage); err != nil {
		return out, err
	}
	point("before_displace")
	if err = check(req.Name); err != nil {
		return out, err
	}
	err = ops.rename(dest, req.Name, dest, backup, unix.RENAME_NOREPLACE)
	if err != nil {
		// A remote filesystem may have completed a failed call. Never infer absence
		// of effects from an errno or remove any possibly displaced evidence.
		touched, preserve = true, true
		return out, err
	}
	touched, preserve = true, true
	rollback := func(cause error) (SidecarResult, error) {
		point("before_restore")
		// Restoration is attempted even after cancellation, always without clobbering.
		e := ops.rename(dest, backup, dest, req.Name, unix.RENAME_NOREPLACE)
		return out, errors.Join(cause, e, ops.sync(dest))
	}
	point("displaced")
	if err = ops.sync(dest); err != nil {
		return rollback(err)
	}
	if err = check(backup); err != nil {
		return rollback(err)
	}
	point("before_publish")
	if err = v.validate(ctx); err != nil {
		return rollback(err)
	}
	if err = check(backup); err != nil {
		return rollback(err)
	}
	if err = verifyStage(stage); err != nil {
		return rollback(err)
	}
	if err = ops.rename(dest, stage, dest, req.Name, unix.RENAME_NOREPLACE); err != nil {
		return rollback(err)
	}
	out = SidecarResult{Disposition: SidecarUncertain, Published: true, Identity: identity(staged)}
	point("published")
	if err = ops.sync(dest); err != nil {
		return out, err
	}
	if err = check(backup); err != nil {
		return out, err
	}
	if err = verifyStage(req.Name); err != nil {
		return out, err
	}
	if err = v.validate(ctx); err != nil {
		return out, err
	}
	out.Disposition = SidecarCreated
	return out, nil
}
