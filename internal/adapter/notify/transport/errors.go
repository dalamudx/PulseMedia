package transport

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net"
)

// Error classifies transport failures without retaining token-bearing URLs or causes.
func Error(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}
