//go:build linux

package rootfs

import (
	"context"
	"math"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

func (s *CapacitySampler) SampleCapacity(ctx context.Context, root ports.TransferRoot) (ports.RootCapacity, error) {
	if err := ctx.Err(); err != nil {
		return ports.RootCapacity{}, err
	}
	cap, ok := root.(*capability)
	if !ok || cap == nil || cap.root == nil {
		return ports.RootCapacity{}, ports.ErrCapacityUnavailable
	}

	var stat unix.Statfs_t
	// Stay synchronous: cancellation cannot end the caller's physical slot early.
	err := unix.Fstatfs(cap.root.fd, &stat)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ports.RootCapacity{}, ctxErr
	}
	if err != nil {
		return ports.RootCapacity{}, ports.ErrCapacityUnavailable
	}
	total, available, err := capacityBytes(stat.Bsize, stat.Blocks, stat.Bavail)
	if err != nil {
		return ports.RootCapacity{}, err
	}
	result := ports.RootCapacity{TotalBytes: total, AvailableBytes: available, SampledAt: s.clock.Now()}
	if err := ctx.Err(); err != nil {
		return ports.RootCapacity{}, err
	}
	return result, nil
}

func capacityBytes(blockSize int64, blocks, available uint64) (int64, int64, error) {
	if blockSize <= 0 || blocks == 0 || available > blocks {
		return 0, 0, ports.ErrCapacityUnavailable
	}
	// Available is bounded by blocks, so this also protects its multiplication.
	if blocks > uint64(math.MaxInt64)/uint64(blockSize) {
		return 0, 0, ports.ErrCapacityUnavailable
	}
	return int64(blocks) * blockSize, int64(available) * blockSize, nil
}
