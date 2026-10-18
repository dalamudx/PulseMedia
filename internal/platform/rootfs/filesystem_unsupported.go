//go:build !linux

package rootfs

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Unsupported hosts fail closed. Never fall back to absolute path operations.
type Filesystem struct{}

func NewFilesystem() *Filesystem { return &Filesystem{} }

var _ ports.TransferFilesystem = (*Filesystem)(nil)
var _ ports.ScanFilesystem = (*Filesystem)(nil)

func unsupported(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ports.ErrUnavailable
}
func (*Filesystem) OpenRoot(ctx context.Context, _ string) (ports.TransferRoot, error) {
	return nil, unsupported(ctx)
}
func (*Filesystem) LockRoots(ctx context.Context, _ ...string) (map[string]ports.TransferRoot, func(), error) {
	return nil, nil, unsupported(ctx)
}
func (*Filesystem) Hardlink(ctx context.Context, _ ports.TransferRoot, _ string, _ ports.TransferRoot, _ string, _ ports.FileIdentity) error {
	return unsupported(ctx)
}
func (*Filesystem) Walk(ctx context.Context, _ ports.TransferRoot, _ func(string, ports.FileIdentity) error) error {
	return unsupported(ctx)
}
