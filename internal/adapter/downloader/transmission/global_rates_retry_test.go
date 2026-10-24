package transmission

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestGlobalTransferRatesRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		wait         time.Duration
	}{
		{"minute", "60", time.Minute},
		{"maximum", "86400", 24 * time.Hour},
		{"absent", "", 0},
		{"zero", "0", 0},
		{"negative", "-1", 0},
		{"excessive", "86401", 0},
		{"overflow", "9223372036854775808", 0},
		{"invalid", "private-not-a-duration", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			rates, err := client.ReadGlobalRates(t.Context())
			var limited *ports.RateLimitError
			if !errors.As(err, &limited) || limited.RetryAfter != tc.wait {
				t.Fatal("native rate limit lost bounded retry advice", err)
			}
			if rates.DownloadBytesPerSecond != nil || rates.UploadBytesPerSecond != nil || calls.Load() != 1 {
				t.Fatal("rate limit returned values or retried automatically")
			}
			if limited.Reason != "download RPC rate limited" {
				t.Fatal("upstream detail entered public error")
			}
		})
	}
}
