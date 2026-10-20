//go:build !linux

package rootfs

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.MediaSidecarFilesystem = (*Filesystem)(nil)

func (*Filesystem) InspectSidecarDirectory(ctx context.Context, _ ports.TransferRoot, _ string) (ports.SidecarDirectoryIdentity, error) {
	return ports.SidecarDirectoryIdentity{}, unsupported(ctx)
}

func (*Filesystem) PublishSidecar(ctx context.Context, _ ports.TransferRoot, _ ports.SidecarRequest) (ports.SidecarResult, error) {
	return ports.SidecarResult{Disposition: ports.SidecarNotPublished}, unsupported(ctx)
}
