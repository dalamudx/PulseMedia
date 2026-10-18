package ports

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrTimeout            = errors.New("external operation timed out")
	ErrUnavailable        = errors.New("external service is temporarily unavailable")
	ErrAuthRequired       = errors.New("external service authentication required or expired")
	ErrBlocked            = errors.New("external request blocked by remote host (Cloudflare/WAF/IP ban)")
	ErrBadResponse        = errors.New("unexpected or malformed response from external service")
	ErrNotFound           = errors.New("resource not found on external service")
	ErrRejected           = errors.New("external operation was rejected by target service")
	ErrStaleDeliveryLease = errors.New("outbox delivery lease expired or held by another owner: write rejected")
)

// RateLimitError represents an HTTP 429 or provider rate limit with retry advice.
type RateLimitError struct {
	RetryAfter time.Duration
	Reason     string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited by remote service (retry after %v): %s", e.RetryAfter, e.Reason)
	}
	return fmt.Sprintf("rate limited by remote service: %s", e.Reason)
}

func NewRateLimitError(retryAfter time.Duration, reason string) error {
	return &RateLimitError{
		RetryAfter: retryAfter,
		Reason:     reason,
	}
}

func IsRateLimited(err error) (*RateLimitError, bool) {
	var rle *RateLimitError
	if errors.As(err, &rle) {
		return rle, true
	}
	return nil, false
}
