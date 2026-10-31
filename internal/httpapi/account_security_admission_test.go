package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func securityAdmissionRequest(ctx context.Context, path, role string) *http.Request {
	r := httptest.NewRequest("POST", path, nil)
	if role != "" {
		ctx = context.WithValue(ctx, actorKey, &ports.Actor{Type: ports.ActorTypeUser, Role: role})
	}
	return r.WithContext(ctx)
}
func TestAccountSecurityAdmissionBudgetAndRefill(t *testing.T) {
	c := clock.NewMock(time.Now())
	calls := 0
	h := AccountSecurityAdmission(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }))
	request := func(path string) *httptest.ResponseRecorder {
		r := securityAdmissionRequest(t.Context(), path, ports.RoleAdmin)
		r.Header.Set("X-Forwarded-For", "203.0.113.7")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 10; i++ {
		if w := request("/api/v1/auth/mfa/confirm"); w.Code != 204 {
			t.Fatal("attempt rejected early", i, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/accounts", "/api/v1/accounts/00000000-0000-4000-8000-000000000001/password", "/api/v1/auth/password", "/api/v1/auth/mfa/setup", "/api/v1/auth/mfa/disable", "/api/v1/auth/mfa/recovery-codes", "/api/v1/auth/sessions/revoke-others"} {
		if w := request(path); w.Code != 429 || w.Header().Get("Retry-After") != "6" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("security budget bypass", path, w.Code)
		}
	}
	if calls != 10 {
		t.Fatal("rejected work reached handler")
	}
	if w := request("/api/v1/auth/login"); w.Code != 204 {
		t.Fatal("security exhausted login budget")
	}
	c.Advance(6 * time.Second)
	if w := request("/api/v1/accounts"); w.Code != 204 {
		t.Fatal("budget did not refill")
	}
}
func TestAccountSecurityAdmissionConcurrentCancellationBound(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := AccountSecurityAdmission(clock.NewMock(time.Now()))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release; w.WriteHeader(204) }))
	defer func() { close(release); wg.Wait() }()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), securityAdmissionRequest(ctx, "/api/v1/auth/mfa/confirm", ports.RoleAdmin))
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("slot did not start")
		}
	}
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, securityAdmissionRequest(t.Context(), "/api/v1/auth/mfa/disable", ports.RoleAdmin))
	if w.Code != 429 {
		t.Fatal("cancel released still-running physical verification slot", w.Code)
	}
}
func TestAccountSecurityAdmissionSkipsCheapDenialsAndCancelledWork(t *testing.T) {
	calls := 0
	h := AccountSecurityAdmission(clock.NewMock(time.Now()))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/api/v1/accounts" {
			w.WriteHeader(403)
		} else {
			w.WriteHeader(204)
		}
	}))
	for i := 0; i < 20; i++ {
		for _, role := range []string{"", ports.RoleMember} {
			h.ServeHTTP(httptest.NewRecorder(), securityAdmissionRequest(t.Context(), "/api/v1/accounts", role))
			h.ServeHTTP(httptest.NewRecorder(), securityAdmissionRequest(t.Context(), "/api/v1/accounts/00000000-0000-4000-8000-000000000001/password", role))
		}
	}
	before := calls
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, securityAdmissionRequest(ctx, "/api/v1/auth/password", ports.RoleAdmin))
	if w.Code != 408 || calls != before {
		t.Fatal("cancelled request did credential work")
	}
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, securityAdmissionRequest(t.Context(), "/api/v1/auth/mfa/confirm", ports.RoleAdmin))
		if w.Code != 204 {
			t.Fatal("cheap denial exhausted security budget", i, w.Code)
		}
	}
}
