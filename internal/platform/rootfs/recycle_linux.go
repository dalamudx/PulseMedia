//go:build linux

package rootfs

import (
	"context"
	"errors"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

var _ ports.RecycleFilesystem = (*Filesystem)(nil)

func (f *Filesystem) Recycle(ctx context.Context, root ports.TransferRoot, source, target string, frozen ports.FileIdentity) error {
	capability, ok := root.(*capability)
	if !ok {
		return ports.ErrFileIdentity
	}
	return Recycle(ctx, capability.root, source, target, frozen)
}

// Recycle publishes a no-replace rename in the controlled recycle directory.
// Both parents are FD capabilities, and the source identity remains pinned until
// the rename, postcondition checks, and directory fsyncs have returned.
func Recycle(ctx context.Context, root *Root, source, target string, frozen Identity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceParts, sourceErr := parts(source)
	targetParts, targetErr := parts(target)
	if root == nil || sourceErr != nil || targetErr != nil || len(sourceParts) == 0 || len(targetParts) < 2 || sourceParts[0] == ".pulsemedia_recycle" || targetParts[0] != ".pulsemedia_recycle" {
		return ErrEscape
	}
	source, target = strings.Join(sourceParts, "/"), strings.Join(targetParts, "/")
	file, actual, err := root.source(source)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if actual != frozen {
		return ErrIdentity
	}
	sourceParent, sourceName, err := root.parent(source, false)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(sourceParent) }()
	targetParent, targetName, err := root.parent(target, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(targetParent) }()
	if now, err := root.Inspect(source); err != nil || now != frozen {
		return ErrIdentity
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat2(sourceParent, sourceName, targetParent, targetName, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrConflict
		}
		return err
	}
	var after unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &after); err != nil {
		return err
	}
	if identity(after) != frozen {
		return ErrIdentity
	}
	if now, err := root.Inspect(target); err != nil || now != frozen {
		return ErrIdentity
	}
	if err := unix.Fstatat(sourceParent, sourceName, &after, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return ErrConflict
	}
	if err := unix.Fsync(targetParent); err != nil {
		return err
	}
	if err := unix.Fsync(sourceParent); err != nil {
		return err
	}
	return ctx.Err()
}
