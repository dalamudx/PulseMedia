//go:build !linux

package rootfs

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (*Filesystem) HashSubtitleVideo(ctx context.Context, _ ports.TransferRoot, _ string, _ ports.FileIdentity) (ports.SubtitleVideoHash, error) {
	return ports.SubtitleVideoHash{}, unsupported(ctx)
}
func (*Filesystem) PublishSubtitle(ctx context.Context, _ ports.TransferRoot, _ ports.SidecarRequest) (ports.SidecarResult, error) {
	return ports.SidecarResult{Disposition: ports.SidecarNotPublished}, unsupported(ctx)
}
func (*Root) PublishSubtitle(ctx context.Context, _ SidecarRequest) (SidecarResult, error) {
	return SidecarResult{Disposition: SidecarNotPublished}, unsupported(ctx)
}
