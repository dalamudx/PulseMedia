package rootfs

import "github.com/dalamudx/PulseMedia/internal/ports"

// CapacitySampler observes capacity under the caller's existing physical slot.
type CapacitySampler struct {
	clock ports.Clock
}

func NewCapacitySampler(clock ports.Clock) *CapacitySampler {
	return &CapacitySampler{clock: clock}
}

var _ ports.RootCapacitySampler = (*CapacitySampler)(nil)
