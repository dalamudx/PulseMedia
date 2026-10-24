//go:build linux

package rootfs

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

var _ ports.VerifiedTransferFilesystem = (*Filesystem)(nil)

const transferStaging = ".pulsemedia_transfers"

// Private operation directories bind staging to a stable, persisted step UUID.
// No existing user target is opened for writing, truncated or removed.
func privateTransferDir(parent int, name string) (int, error) {
	err := unix.Mkdirat(parent, name, 0700)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, err
	}
	if err == nil {
		if err = unix.Fsync(parent); err != nil {
			return -1, err
		}
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return -1, ErrSymlink
		}
		return -1, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err == nil && (st.Uid != uint32(unix.Geteuid()) || st.Mode&0777 != 0700) {
		err = ErrConflict
	}
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func transferDir(root *Root, key uuid.UUID) (int, error) {
	base, err := privateTransferDir(root.fd, transferStaging)
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(base) }()
	return privateTransferDir(base, key.String())
}

func validTransferPath(rel string) error {
	p, err := parts(rel)
	if err != nil {
		return err
	}
	if len(p) == 0 || p[0] == transferStaging || p[0] == ".pulsemedia_recycle" {
		return ErrEscape
	}
	return nil
}

type transferReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r transferReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func contentOf(ctx context.Context, file *os.File, frozen Identity) (ports.TransferContent, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ports.TransferContent{}, err
	}
	hash := sha256.New()
	count, err := io.CopyBuffer(hash, transferReader{ctx, file}, make([]byte, 128*1024))
	if err != nil {
		return ports.TransferContent{}, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &st); err != nil {
		return ports.TransferContent{}, err
	}
	if count != frozen.Size || identity(st) != frozen {
		return ports.TransferContent{}, ErrIdentity
	}
	if err = ctx.Err(); err != nil {
		return ports.TransferContent{}, err
	}
	out := ports.TransferContent{File: frozen}
	copy(out.SHA256[:], hash.Sum(nil))
	return out, nil
}

func (f *Filesystem) InspectContent(ctx context.Context, root ports.TransferRoot, rel string, frozen ports.FileIdentity) (ports.TransferContent, error) {
	c, ok := root.(*capability)
	if !ok {
		return ports.TransferContent{}, ErrIdentity
	}
	file, current, err := c.root.source(rel)
	if err != nil {
		return ports.TransferContent{}, err
	}
	defer func() { _ = file.Close() }()
	if current != frozen {
		return ports.TransferContent{}, ErrIdentity
	}
	out, err := contentOf(ctx, file, frozen)
	if err != nil {
		return ports.TransferContent{}, err
	}
	if now, e := c.root.Inspect(rel); e != nil || now != frozen {
		return ports.TransferContent{}, ErrIdentity
	}
	return out, nil
}

// transferRenameError preserves no-overwrite semantics on filesystems without rename flags.
func transferRenameError(err error) error {
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return errors.Join(ports.ErrFileUnsupported, err)
	}
	return err
}
func cloneUnavailable(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS)
}

func (f *Filesystem) ProbeReflink(ctx context.Context, source ports.TransferRoot, rel string, target ports.TransferRoot, frozen ports.FileIdentity, key uuid.UUID) (supported bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	src, ok := source.(*capability)
	if !ok || key == uuid.Nil {
		return false, ErrIdentity
	}
	dst, ok := target.(*capability)
	if !ok {
		return false, ErrIdentity
	}
	if err := validTransferPath(rel); err != nil {
		return false, err
	}
	input, id, err := src.root.source(rel)
	if err != nil {
		return false, err
	}
	defer func() { _ = input.Close() }()
	if id != frozen {
		return false, ErrIdentity
	}
	dir, err := transferDir(dst.root, key)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(dir) }()
	fd, err := unix.Openat(dir, "reflink-probe", unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, unix.Close(fd), unix.Unlinkat(dir, "reflink-probe", 0), unix.Fsync(dir))
		if err != nil {
			supported = false
		}
	}()
	err = unix.IoctlFileClone(fd, int(input.Fd()))
	if cloneUnavailable(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if now, e := src.root.Inspect(rel); e != nil || now != frozen {
		return false, ErrIdentity
	}
	return true, ctx.Err()
}

func verifiedTarget(ctx context.Context, dst *Root, rel string, expected ports.TransferContent) (ports.TransferContent, error) {
	file, id, err := dst.source(rel)
	if err != nil {
		return ports.TransferContent{}, err
	}
	defer func() { _ = file.Close() }()
	if id.Size != expected.File.Size || id.Device == expected.File.Device && id.Inode == expected.File.Inode {
		return ports.TransferContent{}, ErrConflict
	}
	out, err := contentOf(ctx, file, id)
	if err != nil {
		return ports.TransferContent{}, err
	}
	if out.SHA256 != expected.SHA256 {
		return ports.TransferContent{}, ErrConflict
	}
	if err = file.Sync(); err != nil {
		return ports.TransferContent{}, err
	}
	if now, e := dst.Inspect(rel); e != nil || now != id {
		return ports.TransferContent{}, ErrIdentity
	}
	return out, nil
}

func removeTransferStage(dir int) error {
	var st unix.Stat_t
	err := unix.Fstatat(dir, "content", &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(unix.Geteuid()) || st.Mode&0777 != 0600 {
		return ErrConflict
	}
	if err = unix.Unlinkat(dir, "content", 0); err != nil {
		return err
	}
	return unix.Fsync(dir)
}

// A move first publishes and syncs the verified destination. The original is
// then renamed into a private directory, checked, and removed there. An unknown
// identity is retained for review; no foreign file is deleted by its old path.
func finishVerifiedMove(ctx context.Context, src *Root, rel string, expected ports.TransferContent, key uuid.UUID) error {
	dir, err := transferDir(src, key)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dir) }()
	parent, name, err := src.parent(rel, false)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	var staged unix.Stat_t
	stagedErr := unix.Fstatat(dir, "move-source", &staged, unix.AT_SYMLINK_NOFOLLOW)
	current, currentErr := src.Inspect(rel)
	if currentErr == nil {
		if current != expected.File || !errors.Is(stagedErr, unix.ENOENT) {
			return ErrConflict
		}
		input, id, e := src.source(rel)
		if e != nil {
			return e
		}
		defer func() { _ = input.Close() }()
		if id != expected.File {
			return ErrIdentity
		}
		proof, e := contentOf(ctx, input, expected.File)
		if e != nil {
			return e
		}
		if proof.SHA256 != expected.SHA256 {
			return ErrIdentity
		}
		if now, e := src.Inspect(rel); e != nil || now != expected.File {
			return ErrIdentity
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = unix.Renameat2(parent, name, dir, "move-source", unix.RENAME_NOREPLACE); err != nil {
			return transferRenameError(err)
		}
		if err = unix.Fstatat(dir, "move-source", &staged, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
	} else if !errors.Is(currentErr, os.ErrNotExist) {
		return currentErr
	} else if errors.Is(stagedErr, unix.ENOENT) {
		return unix.Fsync(parent)
	} else if stagedErr != nil {
		return stagedErr
	}
	if staged.Mode&unix.S_IFMT != unix.S_IFREG || identity(staged) != expected.File {
		return ErrIdentity
	}
	fd, e := unix.Openat(dir, "move-source", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	quarantined := os.NewFile(uintptr(fd), "move-source")
	defer func() { _ = quarantined.Close() }()
	proof, e := contentOf(ctx, quarantined, expected.File)
	if e != nil {
		return e
	}
	if proof.SHA256 != expected.SHA256 {
		return ErrIdentity
	}
	if err = unix.Fsync(parent); err != nil {
		return err
	}
	if err = unix.Fsync(dir); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = unix.Unlinkat(dir, "move-source", 0); err != nil {
		return err
	}
	return unix.Fsync(dir)
}

func (f *Filesystem) TransferVerified(ctx context.Context, source ports.TransferRoot, sourceRel string, target ports.TransferRoot, targetRel string, expected ports.TransferContent, action string, key uuid.UUID) (out ports.VerifiedTransferResult, err error) {
	defer func() {
		if errors.Is(err, unix.ENOSPC) {
			err = errors.Join(ports.ErrFileNoSpace, err)
		}
	}()
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if action != "copy" && action != "reflink" && action != "move" {
		return out, ports.ErrFileUnsupported
	}
	src, ok := source.(*capability)
	if !ok || key == uuid.Nil {
		return out, ErrIdentity
	}
	dst, ok := target.(*capability)
	if !ok {
		return out, ErrIdentity
	}
	if err = validTransferPath(sourceRel); err != nil {
		return out, err
	}
	if err = validTransferPath(targetRel); err != nil {
		return out, err
	}
	srcRoot, e := src.root.Identity()
	if e != nil {
		return out, e
	}
	dstRoot, e := dst.root.Identity()
	if e != nil {
		return out, e
	}
	sp, _ := parts(sourceRel)
	tp, _ := parts(targetRel)
	if srcRoot.Device == dstRoot.Device && srcRoot.Inode == dstRoot.Inode && strings.Join(sp, "/") == strings.Join(tp, "/") {
		return out, ErrConflict
	}
	parent, name, err := dst.root.parent(targetRel, true)
	if err != nil {
		return out, err
	}
	defer func() { _ = unix.Close(parent) }()
	dir, err := transferDir(dst.root, key)
	if err != nil {
		return out, err
	}
	defer func() { _ = unix.Close(dir) }()
	out.Target, err = verifiedTarget(ctx, dst.root, targetRel, expected)
	if errors.Is(err, os.ErrNotExist) {
		input, id, e := src.root.source(sourceRel)
		if e != nil {
			return out, e
		}
		defer func() { _ = input.Close() }()
		if id != expected.File {
			return out, ErrIdentity
		}
		// Recreate an interrupted private stage rather than truncating a possible
		// hardlink. Exclusive creation cannot open a foreign file for writing.
		if e = removeTransferStage(dir); e != nil {
			return out, e
		}
		fd, e := unix.Openat(dir, "content", unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if e != nil {
			return out, e
		}
		staged := os.NewFile(uintptr(fd), "content")
		defer func() { _ = staged.Close() }()
		var info unix.Stat_t
		if e = unix.Fstat(fd, &info); e != nil {
			return out, e
		}
		if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uint32(unix.Geteuid()) || info.Mode&0777 != 0600 {
			return out, ErrConflict
		}
		if action == "reflink" {
			if e = unix.IoctlFileClone(fd, int(input.Fd())); e != nil {
				if cloneUnavailable(e) {
					return out, ports.ErrFileUnsupported
				}
				return out, e
			}
		} else {
			hash := sha256.New()
			count, e := io.CopyBuffer(io.MultiWriter(staged, hash), transferReader{ctx, input}, make([]byte, 128*1024))
			if e != nil {
				return out, e
			}
			var digest [32]byte
			copy(digest[:], hash.Sum(nil))
			if count != expected.File.Size || digest != expected.SHA256 {
				return out, ErrIdentity
			}
		}
		if e = staged.Sync(); e != nil {
			return out, e
		}
		if e = unix.Fstat(fd, &info); e != nil {
			return out, e
		}
		proof, e := contentOf(ctx, staged, identity(info))
		if e != nil {
			return out, e
		}
		if proof.File.Size != expected.File.Size || proof.SHA256 != expected.SHA256 {
			return out, ErrIdentity
		}
		if now, e := src.root.Inspect(sourceRel); e != nil || now != expected.File {
			return out, ErrIdentity
		}
		if e = ctx.Err(); e != nil {
			return out, e
		}
		e = unix.Renameat2(dir, "content", parent, name, unix.RENAME_NOREPLACE)
		if e != nil && !errors.Is(e, unix.EEXIST) {
			return out, transferRenameError(e)
		}
		if e = unix.Fsync(dir); e != nil {
			return out, e
		}
		out.Target, err = verifiedTarget(ctx, dst.root, targetRel, expected)
	}
	if err != nil {
		return out, err
	}
	if err = unix.Fsync(parent); err != nil {
		return out, err
	}
	if err = removeTransferStage(dir); err != nil {
		return out, err
	}
	if action == "move" {
		if err = finishVerifiedMove(ctx, src.root, sourceRel, expected, key); err != nil {
			return out, err
		}
		out.SourceRemoved = true
	}
	return out, ctx.Err()
}

// ObserveTransfer establishes each action's complete postcondition without
// creating stages, replaying writes, or removing an incomplete move source.
func (f *Filesystem) ObserveTransfer(ctx context.Context, source ports.TransferRoot, sourceRel string, target ports.TransferRoot, targetRel string, expected ports.TransferContent, action string, key uuid.UUID) (out ports.VerifiedTransferResult, err error) {
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if action != "copy" && action != "reflink" && action != "move" {
		return out, ports.ErrFileUnsupported
	}
	src, ok := source.(*capability)
	if !ok || key == uuid.Nil {
		return out, ErrIdentity
	}
	dst, ok := target.(*capability)
	if !ok {
		return out, ErrIdentity
	}
	if err = validTransferPath(sourceRel); err != nil {
		return out, err
	}
	if err = validTransferPath(targetRel); err != nil {
		return out, err
	}
	out.Target, err = verifiedTarget(ctx, dst.root, targetRel, expected)
	if err != nil {
		return out, err
	}
	if action == "move" {
		if _, e := src.root.Inspect(sourceRel); !errors.Is(e, os.ErrNotExist) {
			if e != nil {
				return out, e
			}
			return out, ErrConflict
		}
		staged := transferStaging + "/" + key.String() + "/move-source"
		if _, e := src.root.Inspect(staged); !errors.Is(e, os.ErrNotExist) {
			if e != nil {
				return out, e
			}
			return out, ErrConflict
		}
		out.SourceRemoved = true
	} else {
		proof, e := f.InspectContent(ctx, source, sourceRel, expected.File)
		if e != nil {
			return out, e
		}
		if proof.SHA256 != expected.SHA256 {
			return out, ErrIdentity
		}
	}
	return out, ctx.Err()
}
