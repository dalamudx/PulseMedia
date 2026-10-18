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
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
)

func setupSettingsHTTP(t *testing.T) (*auth.Service, *settings.Service, string, http.Handler) {
	t.Helper()
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsPath); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	tempDir, err := os.MkdirTemp("", "pulsemedia-settings-http-test-*")
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
	settingsSvc := settings.NewService(postgres.NewSettingsStore(a.DBPool(), gen))

	// Create initial admin
	_, adminToken, err := authSvc.SetupAdmin(ctx, "admin", "AdminPass12345!")
	if err != nil {
		t.Fatalf("failed to create admin: %v", err)
	}

	serverHandler := httpapi.NewServerHandler(a, authSvc, settingsSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(a, serverHandler, authSvc, nil)

	return authSvc, settingsSvc, adminToken, router
}

func TestSettingsHTTPOptimisticLockAndIdempotency(t *testing.T) {
	_, _, adminToken, router := setupSettingsHTTP(t)

	// 1. Initial setting update with Idempotency-Key
	payload1 := []byte(`{"expected_version":0,"value":{"site_name":"PulseMedia"}}`)
	req1 := httptest.NewRequest(http.MethodPut, "/api/v1/settings/site.config", bytes.NewReader(payload1))
	req1.Header.Set("Authorization", "Bearer "+adminToken)
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Idempotency-Key", "idemp_site_001")
	rec1 := httptest.NewRecorder()

	router.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 on initial setting update, got %d: %s", rec1.Code, rec1.Body.String())
	}

	var item1 httpapi.SettingItem
	if err := json.Unmarshal(rec1.Body.Bytes(), &item1); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if item1.Version != 1 {
		t.Fatalf("expected version 1, got %d", item1.Version)
	}

	// 2. Replay with same Idempotency-Key and same payload: returns 200 without version bump
	reqReplay := httptest.NewRequest(http.MethodPut, "/api/v1/settings/site.config", bytes.NewReader(payload1))
	reqReplay.Header.Set("Authorization", "Bearer "+adminToken)
	reqReplay.Header.Set("Content-Type", "application/json")
	reqReplay.Header.Set("Idempotency-Key", "idemp_site_001")
	recReplay := httptest.NewRecorder()

	router.ServeHTTP(recReplay, reqReplay)

	if recReplay.Code != http.StatusOK {
		t.Fatalf("expected 200 on replay, got: %d", recReplay.Code)
	}

	var itemReplay httpapi.SettingItem
	_ = json.Unmarshal(recReplay.Body.Bytes(), &itemReplay)
	if itemReplay.Version != 1 {
		t.Fatalf("expected version to remain 1 on replay, got: %d", itemReplay.Version)
	}

	// 3. Version conflict returns 412 Precondition Failed
	payloadWrongVersion := []byte(`{"expected_version":999,"value":{"site_name":"New Name"}}`)
	reqConflict := httptest.NewRequest(http.MethodPut, "/api/v1/settings/site.config", bytes.NewReader(payloadWrongVersion))
	reqConflict.Header.Set("Authorization", "Bearer "+adminToken)
	reqConflict.Header.Set("Content-Type", "application/json")
	recConflict := httptest.NewRecorder()

	router.ServeHTTP(recConflict, reqConflict)

	if recConflict.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 Precondition Failed on version mismatch, got: %d", recConflict.Code)
	}

	var errResp httpapi.ErrorResponse
	if err := json.Unmarshal(recConflict.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if errResp.Code != "VERSION_CONFLICT" {
		t.Fatalf("expected code VERSION_CONFLICT, got: %s", errResp.Code)
	}
}
