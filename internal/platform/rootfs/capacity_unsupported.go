//go:build !linux

package rootfs

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (*CapacitySampler) SampleCapacity(ctx context.Context, _ ports.TransferRoot) (ports.RootCapacity, error) {
	if err := ctx.Err(); err != nil {
		return ports.RootCapacity{}, err
	}
	return ports.RootCapacity{}, ports.ErrCapacityUnavailable
}
