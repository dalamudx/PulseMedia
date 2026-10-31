package httpapi

import (
	"net/http"
	"strings"
)

// Apply to early authentication/CSRF/parser failures as well as successful responses.
func reviewBusinessNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/api/v1/subscriptions" || strings.HasPrefix(p, "/api/v1/subscriptions/") {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Pragma", "no-cache")
		}
		if strings.HasPrefix(p, "/api/v1/accounts") || strings.HasPrefix(p, "/api/v1/auth/") || strings.Contains(p, "/classification") || p == "/api/v1/settings/seerr" || p == "/api/v1/seerr/receipts" || p == "/api/v1/integrations/seerr" || (strings.HasPrefix(p, "/api/v1/subscriptions/") && strings.HasSuffix(p, "/execution")) || (strings.HasPrefix(p, "/api/v1/operations/") && (strings.Contains(p, "/download-control") || strings.Contains(p, "/seeding-observations"))) || (strings.HasPrefix(p, "/api/v1/library/files/") && strings.Contains(p, "/subtitle-candidates")) {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Pragma", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}
