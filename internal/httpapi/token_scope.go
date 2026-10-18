package httpapi

import (
	"net/http"
	"strings"
)

// APITokenScopeMiddleware attenuates an API credential. This V1 slice only
// supports explicit read queries; a write or role-named scope is not command
// authority. Handler/usecase role and object checks still apply to allowed reads.
func APITokenScopeMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := GetActor(r.Context())
			if actor == nil || actor.Scopes == nil || (r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/")) {
				next.ServeHTTP(w, r)
				return
			}
			query := r.Method == http.MethodGet || r.Method == http.MethodHead
			ownIdentity := r.URL.Path == "/api/v1/auth/me" || r.URL.Path == "/api/v1/auth/status"
			read := false
			for _, scope := range actor.Scopes {
				if scope == "read" {
					read = true
					break
				}
			}
			if !query || (!ownIdentity && !read) {
				WriteJSONError(w, http.StatusForbidden, "FORBIDDEN", "API token does not authorize this action", GetRequestID(r.Context()))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
