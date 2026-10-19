package rootfs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type capacityTestClock struct {
	now   time.Time
	calls int
	onNow func()
}

func (c *capacityTestClock) Now() time.Time {
	c.calls++
	if c.onNow != nil {
		c.onNow()
	}
	return c.now
}

func (c *capacityTestClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

// Embedded methods panic if the sampler tries to use a foreign capability.
type foreignCapacityRoot struct{ ports.TransferRoot }

func TestCapacitySamplerRejectsForeignAndNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		root ports.TransferRoot
	}{
		{name: "nil"},
		{name: "foreign", root: &foreignCapacityRoot{}},
		{name: "typed_nil_foreign", root: (*foreignCapacityRoot)(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &capacityTestClock{}
			got, err := NewCapacitySampler(clock).SampleCapacity(context.Background(), tc.root)
			if !errors.Is(err, ports.ErrCapacityUnavailable) || got != (ports.RootCapacity{}) || clock.calls != 0 {
				t.Fatalf("sample = %+v, %v; clock calls = %d", got, err, clock.calls)
			}
		})
	}
}

func TestCapacitySamplerCanceled(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "canceled", ctx: canceled},
		{name: "deadline", ctx: expired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &capacityTestClock{}
			got, err := NewCapacitySampler(clock).SampleCapacity(tc.ctx, &foreignCapacityRoot{})
			if !errors.Is(err, tc.ctx.Err()) || got != (ports.RootCapacity{}) || clock.calls != 0 {
				t.Fatalf("sample = %+v, %v; clock calls = %d", got, err, clock.calls)
			}
		})
	}
}
