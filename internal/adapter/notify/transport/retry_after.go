package transport

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryAfterSeconds caps before converting, so large valid advice never wraps
// or falls back to a much shorter retry interval.
func RetryAfterSeconds(seconds int64, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(min(seconds, 86400)) * time.Second
}

func RetryAfter(value string, now time.Time, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return RetryAfterSeconds(seconds, fallback)
	}
	if at, err := http.ParseTime(value); err == nil {
		if wait := at.Sub(now); wait > 0 {
			// Durable retry deadlines use milliseconds; do not round down to zero.
			wait = min(wait, 24*time.Hour)
			return (wait + time.Millisecond - 1) / time.Millisecond * time.Millisecond
		}
	}
	return fallback
}
