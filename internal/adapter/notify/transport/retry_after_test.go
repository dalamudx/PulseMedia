package transport_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/notify/transport"
)

func TestRetryAfterHeaderBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	fallback := time.Minute
	for _, tc := range []struct {
		name, value string
		now         time.Time
		want        time.Duration
	}{
		{"seconds", "90", now, 90 * time.Second},
		{"whitespace", " 90 ", now, 90 * time.Second},
		{"cap", "86400", now, 24 * time.Hour},
		{"over_cap", "86401", now, 24 * time.Hour},
		{"int64_max", "9223372036854775807", now, 24 * time.Hour},
		{"overflow", "9223372036854775808", now, fallback},
		{"zero", "0", now, fallback},
		{"negative", "-1", now, fallback},
		{"missing", "", now, fallback},
		{"malformed", "not a duration", now, fallback},
		{"date", now.Add(90 * time.Second).Format(http.TimeFormat), now, 90 * time.Second},
		{"long_date", now.Add(48 * time.Hour).Format(http.TimeFormat), now, 24 * time.Hour},
		{"past", now.Add(-time.Second).Format(http.TimeFormat), now, fallback},
		{"current", now.Format(http.TimeFormat), now, fallback},
		{"sub_ms", now.Add(time.Second).Format(http.TimeFormat), now.Add(time.Second - 500*time.Microsecond), time.Millisecond},
		{"fraction_ms", now.Add(time.Second).Format(http.TimeFormat), now.Add(time.Second - 1500*time.Microsecond), 2 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := transport.RetryAfter(tc.value, tc.now, fallback); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
