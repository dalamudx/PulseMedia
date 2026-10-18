package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPITokenScopeMethodAndPathBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		scopes             []string
		allowed            bool
	}{
		{"session write", "POST", "/api/v1/subscriptions", nil, true},
		{"explicit read", "GET", "/api/v1/subscriptions", []string{"read"}, true},
		{"read head", "HEAD", "/api/v1/subscriptions", []string{"read"}, true},
		{"empty", "GET", "/api/v1/subscriptions", []string{}, false},
		{"case", "GET", "/api/v1/subscriptions", []string{"READ"}, false},
		{"whitespace", "GET", "/api/v1/subscriptions", []string{" read"}, false},
		{"wildcard", "GET", "/api/v1/subscriptions", []string{"*"}, false},
		{"role", "GET", "/api/v1/subscriptions", []string{"admin", "system"}, false},
		{"write scope", "POST", "/api/v1/subscriptions", []string{"write"}, false},
		{"combined write", "PUT", "/api/v1/settings/key", []string{"read", "write"}, false},
		{"delete", "DELETE", "/api/v1/subscriptions/id", []string{"read"}, false},
		{"patch", "PATCH", "/api/v1/settings/key", []string{"read"}, false},
		{"self", "GET", "/api/v1/auth/me", []string{}, true},
		{"self suffix", "GET", "/api/v1/auth/me/other", []string{}, false},
		{"status", "GET", "/api/v1/auth/status", []string{}, true},
		{"UI", "GET", "/subscriptions", []string{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: "owned-user", Role: ports.RoleMember, Scopes: tc.scopes}
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req = req.WithContext(context.WithValue(req.Context(), actorKey, actor))
			rec := httptest.NewRecorder()
			APITokenScopeMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if GetActor(r.Context()) != actor || actor.Role != ports.RoleMember || actor.Type != ports.ActorTypeUser {
					t.Error("scope gate changed user authority")
				}
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(rec, req)
			if called != tc.allowed {
				t.Errorf("handler called=%v, want %v", called, tc.allowed)
			}
			if !tc.allowed && (rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/problem+json") {
				t.Error("denial lost public error contract")
			}
		})
	}
}
