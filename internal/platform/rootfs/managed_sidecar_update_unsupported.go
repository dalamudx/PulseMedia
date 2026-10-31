//go:build !linux

package rootfs

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var _ ports.ManagedSidecarUpdateFilesystem = (*Filesystem)(nil)

func (*Filesystem) ReadManagedSidecar(ctx context.Context, _ ports.TransferRoot, _ ports.SidecarRequest) (ports.ManagedSidecarContent, error) {
	return ports.ManagedSidecarContent{}, unsupported(ctx)
}

func (*Filesystem) ReplaceManagedSidecar(ctx context.Context, _ ports.TransferRoot, _ ports.SidecarRequest, _ ports.ManagedSidecarContent, _ uuid.UUID) (ports.SidecarResult, error) {
	return ports.SidecarResult{Disposition: ports.SidecarNotPublished}, unsupported(ctx)
}
