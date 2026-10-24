package httpapi

import (
	"net/http"
	"strings"
)

// Wrap authentication too: errors contain private observation/access state.
func downloadGlobalRatesNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSuffix(r.URL.Path, "/") == "/api/v1/download-clients/global-rates" {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
