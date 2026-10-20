package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

type checkinConfigAuthRepo struct {
	ports.AuthenticationRepository
	hash    []byte
	user    ports.User
	expires time.Time
	token   bool
	err     error
}

func (r *checkinConfigAuthRepo) FindSession(_ context.Context, hash []byte) (ports.AuthenticationSession, error) {
	if r.err != nil {
		return ports.AuthenticationSession{}, r.err
	}
	if r.token || !bytes.Equal(r.hash, hash) {
		return ports.AuthenticationSession{}, ports.ErrAuthenticationNotFound
	}
	return ports.AuthenticationSession{ID: uuid.New(), User: r.user, ExpiresAt: r.expires}, nil
}
func (r *checkinConfigAuthRepo) FindAPIToken(_ context.Context, hash []byte) (ports.AuthenticationToken, error) {
	if !r.token || !bytes.Equal(r.hash, hash) {
		return ports.AuthenticationToken{}, ports.ErrAuthenticationNotFound
	}
	return ports.AuthenticationToken{User: r.user, ScopesJSON: []byte(`["read","write"]`)}, nil
}
func (r *checkinConfigAuthRepo) TouchSession(context.Context, uuid.UUID) error { return nil }

type checkinConfigClock struct{ now time.Time }

func (c checkinConfigClock) Now() time.Time                  { return c.now }
func (c checkinConfigClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

func TestSiteCheckinConfigRealAuthentication(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, role                    string
		active, token                 bool
		expired, invalid, unavailable bool
		status                        int
	}{
		{name: "admin", role: ports.RoleAdmin, active: true, status: 503},
		{name: "member", role: ports.RoleMember, active: true, status: 403},
		{name: "admin-api-token", role: ports.RoleAdmin, active: true, token: true, status: 403},
		{name: "disabled-admin", role: ports.RoleAdmin, status: 401},
		{name: "expired-admin", role: ports.RoleAdmin, active: true, expired: true, status: 401},
		{name: "invalid-credential", role: ports.RoleAdmin, active: true, invalid: true, status: 401},
		{name: "auth-unavailable", role: ports.RoleAdmin, active: true, unavailable: true, status: 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			nonce := uuid.NewString()
			secrets := crypto.AuthenticationSecrets{}
			repo := &checkinConfigAuthRepo{hash: secrets.HashToken(nonce), user: ports.User{ID: uuid.New(), Role: tt.role, IsActive: tt.active}, expires: now.Add(time.Hour), token: tt.token}
			if tt.expired {
				repo.expires = now
			}
			if tt.unavailable {
				repo.err = ports.ErrUnavailable
			}
			svc := auth.NewService(repo, secrets, checkinConfigClock{now}, nil)
			server, configRepo := checkinConfigFixture()
			configRepo.err = ports.ErrUnavailable
			h := siteCheckinConfigNoStore(AuthMiddleware(svc)(checkinConfigRouter(server, nil)))
			for _, request := range []struct{ method, path, body string }{{"GET", checkinConfigPath, ""}, {"PUT", checkinConfigPath, checkinConfigBody}, {"GET", checkinConfigPath + "/commands/key", ""}} {
				r := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
				r.Header.Set("Content-Type", "application/json")
				if request.method == "PUT" {
					r.Header.Set("Idempotency-Key", "key")
				}
				credential := nonce
				if tt.invalid {
					credential = uuid.NewString()
				}
				r.Header.Set("Authorization", "Bearer "+credential)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tt.status || w.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			wantCalls := 0
			if tt.name == "admin" {
				wantCalls = 3
			}
			if configRepo.calls != wantCalls {
				t.Fatalf("repository calls=%d want=%d", configRepo.calls, wantCalls)
			}
		})
	}
	// Anonymous admission is checked through the actual authentication middleware too.
	h := siteCheckinConfigNoStore(AuthMiddleware(nil)(checkinConfigRouter(&ServerHandler{}, nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, checkinConfigPath, nil))
	if w.Code != 401 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Code)
	}
}
