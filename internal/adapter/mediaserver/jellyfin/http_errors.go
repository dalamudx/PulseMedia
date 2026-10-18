package jellyfin

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net"
	"net/http"
	"strconv"
	"time"
)

func transportFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var failure net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &failure) && failure.Timeout() {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}
func responseFailure(resp *http.Response, accepted ...int) error {
	for _, status := range accepted {
		if resp.StatusCode == status {
			return nil
		}
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ports.ErrAuthRequired
	case http.StatusTooManyRequests:
		var wait time.Duration
		if seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); err == nil && seconds > 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
			wait = time.Duration(seconds) * time.Second
		}
		return ports.NewRateLimitError(wait, "Jellyfin HTTP 429")
	}
	if resp.StatusCode >= 500 {
		return ports.ErrUnavailable
	}
	return ports.ErrBadResponse
}
