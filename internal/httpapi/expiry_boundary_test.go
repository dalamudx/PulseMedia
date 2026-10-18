package httpapi_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

func TestAuthenticationExpiryBoundaryHTTPAndSessionTouch(t *testing.T) {
	application, _, _ := setupAuthHTTP(t)
	ctx := context.Background()
	pool := application.DBPool()
	var databaseNow time.Time
	if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
		t.Fatal(err)
	}
	// Keep the row live by database time, and exercise expiry after lookup using
	// the injected clock. The SQL filter is an additional independent boundary.
	now := databaseNow.Add(time.Hour).Truncate(time.Microsecond)
	service := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, clock.NewMock(now), idgen.New())
	handler := httpapi.NewServerHandler(application, service, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(application, handler, service, nil)
	user, sessionToken, err := service.SetupAdmin(ctx, "owned-expiry-fixture", "OwnedExpiryFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	oldSeen := now.Add(-time.Hour)
	apiToken := "owned-expiry-api-token"
	apiID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO api_token(id,user_id,name,token_hash,scopes,expires_at) VALUES($1,$2,'expiry fixture',$3,'[]',$4)", apiID, user.ID, crypto.HashToken(apiToken), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"session", "api-token"} {
		t.Run(kind, func(t *testing.T) {
			raw := sessionToken
			if kind == "api-token" {
				raw = apiToken
			}
			for _, tc := range []struct {
				name   string
				expiry time.Time
				valid  bool
			}{
				{"past", now.Add(-time.Microsecond), false},
				{"equal", now, false},
				{"future", now.Add(time.Microsecond), true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if kind == "session" {
						if _, err := pool.Exec(ctx, "UPDATE app_session SET expires_at=$1,last_seen_at=$2 WHERE token_hash=$3", tc.expiry, oldSeen, crypto.HashToken(raw)); err != nil {
							t.Fatal(err)
						}
					} else if _, err := pool.Exec(ctx, "UPDATE api_token SET expires_at=$1 WHERE id=$2", tc.expiry, apiID); err != nil {
						t.Fatal(err)
					}
					actor, err := service.AuthenticateToken(ctx, raw)
					if tc.valid {
						if err != nil || actor == nil || actor.Type != ports.ActorTypeUser {
							t.Error("unexpired credential no longer authenticates")
						}
					} else if !errors.Is(err, ports.ErrTokenExpired) || actor != nil {
						t.Error("expired credential still authenticates")
					}
					req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
					req.Header.Set("Authorization", "Bearer "+raw)
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					want := http.StatusUnauthorized
					if tc.valid {
						want = http.StatusOK
					}
					if rec.Code != want {
						t.Errorf("public authentication status: got %d, want %d", rec.Code, want)
					}
					if !tc.valid && rec.Header().Get("Set-Cookie") != "" {
						t.Error("expired credential issued cookie")
					}
					if kind == "session" && !tc.valid {
						var lastSeen time.Time
						if err := pool.QueryRow(ctx, "SELECT last_seen_at FROM app_session WHERE token_hash=$1", crypto.HashToken(raw)).Scan(&lastSeen); err != nil {
							t.Fatal(err)
						}
						if !lastSeen.Equal(oldSeen) {
							t.Error("expired session was touched")
						}
					}
				})
			}
		})
	}
	// An explicitly nonexpiring API token remains supported.
	if _, err := pool.Exec(ctx, "UPDATE api_token SET expires_at=NULL WHERE id=$1", apiID); err != nil {
		t.Fatal(err)
	}
	if actor, err := service.AuthenticateToken(ctx, apiToken); err != nil || actor == nil {
		t.Fatal("nonexpiring token contract regressed")
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{sessionToken, apiToken} {
		if actor, err := service.AuthenticateToken(ctx, raw); (!errors.Is(err, ports.ErrUserDisabled) && !errors.Is(err, ports.ErrUnauthorized)) || actor != nil {
			t.Fatal("disabled user can authenticate")
		}
	}
	var sessions, tokens int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app_session").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM api_token").Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || tokens != 1 {
		t.Fatal("authentication probes created credentials")
	}
}
