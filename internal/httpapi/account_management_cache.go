package httpapi

import "net/http"

// AccountManagementNoStore wraps authentication and CSRF admission too.
func AccountManagementNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accountSecurityPath(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
