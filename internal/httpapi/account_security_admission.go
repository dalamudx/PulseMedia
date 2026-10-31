package httpapi

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"sync"
)

// AccountSecurityAdmission owns a separate bounded reauthentication budget per router.
// Keeping it separate prevents password/settings commands consuming login admission.
func AccountSecurityAdmission(clock ports.Clock) func(http.Handler) http.Handler {
	var mu sync.Mutex
	tokens := float64(10)
	last := clock.Now()
	slots := make(chan struct{}, 2)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			actor := GetActor(r.Context())
			if actor == nil || actor.Scopes != nil || (accountManagementPath(path) && actor.Role != ports.RoleAdmin) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method != "POST" || (path != "/api/v1/accounts" && !accountPasswordResetPath(path) && path != "/api/v1/auth/password" && path != "/api/v1/auth/mfa/setup" && path != "/api/v1/auth/mfa/confirm" && path != "/api/v1/auth/mfa/disable" && path != "/api/v1/auth/mfa/recovery-codes" && path != "/api/v1/auth/sessions/revoke-others") {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Cache-Control", "private, no-store")
			if r.Context().Err() != nil {
				WriteJSONError(w, 408, "REQUEST_CANCELLED", "request cancelled", GetRequestID(r.Context()))
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
				WriteJSONError(w, 429, "AUTH_RATE_LIMITED", "security attempts temporarily limited", GetRequestID(r.Context()))
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
			next.ServeHTTP(w, r)
		})
	}
}
