//go:build linux

package rootfs

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

var _ ports.MediaSidecarFilesystem = (*Filesystem)(nil)

func (*Filesystem) InspectSidecarDirectory(ctx context.Context, root ports.TransferRoot, relative string) (ports.SidecarDirectoryIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ports.SidecarDirectoryIdentity{}, err
	}
	cap, ok := root.(*capability)
	if !ok || cap == nil || cap.root == nil {
		return ports.SidecarDirectoryIdentity{}, ports.ErrFileIdentity
	}
	id, err := cap.root.InspectSidecarDirectory(ctx, relative)
	return id, sidecarPortError(err)
}

func (*Filesystem) PublishSidecar(ctx context.Context, root ports.TransferRoot, req ports.SidecarRequest) (ports.SidecarResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.SidecarResult{Disposition: ports.SidecarNotPublished}, err
	}
	cap, ok := root.(*capability)
	if !ok || cap == nil || cap.root == nil {
		return ports.SidecarResult{Disposition: ports.SidecarNotPublished}, ports.ErrFileIdentity
	}
	out, err := cap.root.PublishSidecar(ctx, req)
	return out, sidecarPortError(err)
}

func sidecarPortError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrFileIdentity, ports.ErrFileSymlink, ports.ErrFileEscape, ports.ErrFileConflict, ports.ErrSidecarRequest} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.ELOOP):
		return ports.ErrFileSymlink
	default:
		return ports.ErrUnavailable
	}
}
