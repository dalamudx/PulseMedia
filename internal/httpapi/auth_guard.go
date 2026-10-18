package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Bound password work across all callers, including callers that rotate addresses
// or spoof forwarding headers. The budget belongs to this router, not a global.
// Ten initial attempts, one replenished every six seconds, two in flight.
func authAdmissionMiddleware(clock ports.Clock) func(http.Handler) http.Handler {
	var mu sync.Mutex
	tokens := float64(10)
	last := clock.Now()
	slots := make(chan struct{}, 2)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
				w.Header().Set("Cache-Control", "no-store")
			}
			if r.Method != http.MethodPost || (r.URL.Path != "/api/v1/auth/setup" && r.URL.Path != "/api/v1/auth/login") {
				next.ServeHTTP(w, r)
				return
			}
			mu.Lock()
			now := clock.Now()
			if now.After(last) {
				tokens = min(10, tokens+now.Sub(last).Seconds()/6)
				last = now
			}
			allowed := tokens >= 1
			if allowed {
				tokens--
			}
			mu.Unlock()
			reject := func() {
				w.Header().Set("Retry-After", "6")
				WriteJSONError(w, http.StatusTooManyRequests, "AUTH_RATE_LIMITED", "authentication attempts temporarily limited", GetRequestID(r.Context()))
			}
			if !allowed {
				reject()
				return
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				reject()
				return
			}
			// Read the entire bounded body before decoding, so trailing whitespace or a
			// second document cannot bypass the size limit after a valid JSON prefix.
			const maxAuthBody = 16 << 10
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBody))
			if err != nil {
				WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid authentication request", GetRequestID(r.Context()))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}
