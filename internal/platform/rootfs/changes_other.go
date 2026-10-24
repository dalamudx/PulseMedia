//go:build !linux

package rootfs

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.RootChangeFilesystem = (*Filesystem)(nil)

func (*Filesystem) WatchRoot(ctx context.Context, _ ports.RootWatchRequest, _ chan<- ports.RootChangeHint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ports.ErrFileWatchUnavailable
}
