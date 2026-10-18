package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

const migrationsPath = "../../migrations"

func setupAuthHTTP(t *testing.T) (*app.App, *auth.Service, http.Handler) {
	t.Helper()
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsPath); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	tempDir, err := os.MkdirTemp("", "pulsemedia-auth-http-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	cfg := config.NewDefault()
	cfg.DataDir = filepath.Join(tempDir, "data")
	cfg.Database.URL = dbDSN

	var logBuf bytes.Buffer
	logger := logging.NewLogger(&logBuf, "debug")
	mockClock := clock.NewMock(time.Now().UTC())
	gen := idgen.New()

	a, err := app.New(cfg,
		app.WithLogger(logger),
		app.WithClock(mockClock),
		app.WithIDGen(gen),
		app.WithVersion("0.1.0-test"),
	)
	if err != nil {
		t.Fatalf("failed to initialize app: %v", err)
	}
	t.Cleanup(func() {
		_ = a.Close()
	})

	authSvc := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, mockClock, gen)
	serverHandler := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(a, serverHandler, authSvc, nil)
	return a, authSvc, router
}

func TestAuthHTTPLifecycle(t *testing.T) {
	_, _, router := setupAuthHTTP(t)

	// 1. Initial status is uninitialized
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	recStatus := httptest.NewRecorder()
	router.ServeHTTP(recStatus, reqStatus)

	if recStatus.Code != http.StatusOK {
		t.Fatalf("expected 200, got: %d", recStatus.Code)
	}
	var authStatus httpapi.AuthStatus
	if err := json.Unmarshal(recStatus.Body.Bytes(), &authStatus); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if authStatus.Initialized {
		t.Fatal("expected uninitialized initially")
	}

	// 2. Setup initial admin
	setupBody := []byte(`{"username":"administrator","password":"StrongAdminPass123!"}`)
	reqSetup := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewReader(setupBody))
	reqSetup.Header.Set("Content-Type", "application/json")
	reqSetup.RemoteAddr = "127.0.0.1:3456"
	reqSetup.Host = "localhost"
	recSetup := httptest.NewRecorder()
	router.ServeHTTP(recSetup, reqSetup)

	if recSetup.Code != http.StatusOK {
		t.Fatalf("expected 200 on setup, got %d: %s", recSetup.Code, recSetup.Body.String())
	}
	var authResp httpapi.AuthResponse
	if err := json.Unmarshal(recSetup.Body.Bytes(), &authResp); err != nil {
		t.Fatalf("failed to unmarshal auth response: %v", err)
	}
	if authResp.Token == "" || authResp.User.Role != httpapi.Admin {
		t.Fatalf("unexpected auth response: %+v", authResp)
	}

	// Verify session cookie was set
	cookies := recSetup.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "pulsemedia_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected pulsemedia_session cookie to be set")
	}
	if !sessionCookie.HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}

	// 3. Second setup must return 409 Conflict (entry point closed)
	reqSetup2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewReader(setupBody))
	reqSetup2.Header.Set("Content-Type", "application/json")
	reqSetup2.RemoteAddr = "127.0.0.1:3456"
	reqSetup2.Host = "localhost"
	recSetup2 := httptest.NewRecorder()
	router.ServeHTTP(recSetup2, reqSetup2)

	if recSetup2.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on second setup, got: %d", recSetup2.Code)
	}

	// 4. Access /api/v1/auth/me with Bearer token
	reqMe := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	reqMe.Header.Set("Authorization", "Bearer "+authResp.Token)
	recMe := httptest.NewRecorder()
	router.ServeHTTP(recMe, reqMe)

	if recMe.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/v1/auth/me, got: %d", recMe.Code)
	}
	var meResp httpapi.UserInfo
	if err := json.Unmarshal(recMe.Body.Bytes(), &meResp); err != nil {
		t.Fatalf("failed to unmarshal me response: %v", err)
	}
	if meResp.Username != "administrator" {
		t.Fatalf("expected administrator, got: %s", meResp.Username)
	}

	// 5. Access /api/v1/auth/me with session cookie
	reqMeCookie := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	reqMeCookie.AddCookie(sessionCookie)
	recMeCookie := httptest.NewRecorder()
	router.ServeHTTP(recMeCookie, reqMeCookie)

	if recMeCookie.Code != http.StatusOK {
		t.Fatalf("expected 200 on cookie auth, got: %d", recMeCookie.Code)
	}

	// 6. Access without credentials returns 401
	reqMeNoAuth := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	recMeNoAuth := httptest.NewRecorder()
	router.ServeHTTP(recMeNoAuth, reqMeNoAuth)

	if recMeNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated me, got: %d", recMeNoAuth.Code)
	}

	// 7. Login with valid credentials
	loginBody := []byte(`{"username":"administrator","password":"StrongAdminPass123!"}`)
	reqLogin := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, reqLogin)

	if recLogin.Code != http.StatusOK {
		t.Fatalf("expected 200 on login, got: %d", recLogin.Code)
	}

	// 8. Logout invalidates session
	reqLogout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	reqLogout.Header.Set("Authorization", "Bearer "+authResp.Token)
	recLogout := httptest.NewRecorder()
	router.ServeHTTP(recLogout, reqLogout)

	if recLogout.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on logout, got: %d", recLogout.Code)
	}

	// Accessing /api/v1/auth/me with old token must now fail with 401
	reqMeAfterLogout := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	reqMeAfterLogout.Header.Set("Authorization", "Bearer "+authResp.Token)
	recMeAfterLogout := httptest.NewRecorder()
	router.ServeHTTP(recMeAfterLogout, reqMeAfterLogout)
	if recMeAfterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got: %d", recMeAfterLogout.Code)
	}
}
