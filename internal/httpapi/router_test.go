package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
)

func setupTestApp(t *testing.T) (*app.App, http.Handler) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "pulsemedia-http-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	cfg := config.NewDefault()
	cfg.DataDir = filepath.Join(tempDir, "data")

	var logBuf bytes.Buffer
	logger := logging.NewLogger(&logBuf, "debug")

	a, err := app.New(cfg, app.WithLogger(logger), app.WithVersion("0.1.0-test"))
	if err != nil {
		t.Fatalf("failed to initialize app: %v", err)
	}
	t.Cleanup(func() {
		_ = a.Close()
	})
	serverHandler := httpapi.NewServerHandler(a, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(a, serverHandler, nil, nil)
	return a, router
}

func TestHealthzEndpoint(t *testing.T) {
	_, router := setupTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got: %d", rec.Code)
	}

	var resp httpapi.HealthStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal healthz response: %v", err)
	}

	if !resp.Alive || resp.Version != "0.1.0-test" {
		t.Fatalf("unexpected healthz payload: %+v", resp)
	}
}

func TestReadyzEndpoint(t *testing.T) {
	a, router := setupTestApp(t)

	// 1. Initially ready
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for readyz, got: %d", rec.Code)
	}

	var resp httpapi.ReadyStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal readyz response: %v", err)
	}
	if !resp.Ready {
		t.Fatalf("expected ready=true, got: %+v", resp)
	}

	// 2. When a component check fails, returns 503 Service Unavailable
	a.SetCheck("database", false, "waiting for database migrations")

	req503 := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec503 := httptest.NewRecorder()
	router.ServeHTTP(rec503, req503)

	if rec503.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for unready service, got: %d", rec503.Code)
	}

	var resp503 httpapi.ReadyStatus
	if err := json.Unmarshal(rec503.Body.Bytes(), &resp503); err != nil {
		t.Fatalf("failed to unmarshal 503 response: %v", err)
	}
	if resp503.Ready {
		t.Fatalf("expected ready=false in 503 response")
	}
	if resp503.Reason == nil || !strings.Contains(*resp503.Reason, "database") {
		t.Fatalf("expected database in unready reason: %v", resp503.Reason)
	}
}

func TestRequestIDPropagation(t *testing.T) {
	_, router := setupTestApp(t)

	// Custom Request ID
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "custom-req-id-1234")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "custom-req-id-1234" {
		t.Fatalf("expected custom X-Request-ID preserved, got: %s", got)
	}

	// Generated Request ID
	req2 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec2 := httptest.NewRecorder()

	router.ServeHTTP(rec2, req2)

	if genID := rec2.Header().Get("X-Request-ID"); genID == "" {
		t.Fatal("expected generated X-Request-ID header in response")
	}
}

func TestUnknownAPIRoutesDoNotReturnHTML(t *testing.T) {
	_, router := setupTestApp(t)

	// Unknown route under /api/v1/
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent/endpoint", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Must be 404
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown route, got: %d", rec.Code)
	}

	// Content-Type must not be HTML
	contentType := rec.Header().Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		t.Fatalf("unknown API route must not return text/html, got: %s", contentType)
	}
	if !strings.Contains(contentType, "application/problem+json") {
		t.Fatalf("expected application/problem+json for API route 404, got: %s", contentType)
	}

	// Body should not contain HTML doctype
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "<!doctype html>") {
		t.Fatalf("unknown API route returned HTML body: %s", body)
	}

	var errResp httpapi.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("expected valid JSON error response for 404, got: %v", err)
	}
	if errResp.Code != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND code, got: %s", errResp.Code)
	}
}

func TestSPAFallbackAndStaticAssets(t *testing.T) {
	_, router := setupTestApp(t)

	// 1. Root path "/" should serve index.html with 200 OK
	reqHome := httptest.NewRequest(http.MethodGet, "/", nil)
	recHome := httptest.NewRecorder()
	router.ServeHTTP(recHome, reqHome)

	if recHome.Code != http.StatusOK {
		t.Fatalf("expected 200 for /, got: %d", recHome.Code)
	}
	if !strings.Contains(recHome.Body.String(), "id=\"root\"") && !strings.Contains(recHome.Body.String(), "id=\"app\"") {
		t.Fatalf("expected embedded index.html on /, got: %s", recHome.Body.String())
	}

	// 2. Client route "/setup" should fall back to index.html with 200 OK
	reqSetup := httptest.NewRequest(http.MethodGet, "/setup", nil)
	recSetup := httptest.NewRecorder()
	router.ServeHTTP(recSetup, reqSetup)

	if recSetup.Code != http.StatusOK {
		t.Fatalf("expected 200 for /setup, got: %d", recSetup.Code)
	}
	if !strings.Contains(recSetup.Body.String(), "id=\"root\"") && !strings.Contains(recSetup.Body.String(), "id=\"app\"") {
		t.Fatalf("expected SPA fallback on /setup, got: %s", recSetup.Body.String())
	}
}

func TestRecoveryDoesNotLeakInternalDetails(t *testing.T) {
	a, _ := setupTestApp(t)

	// Handler that panics with secret database credentials
	panickingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("database connection failure at postgres://admin:SuperSecretPass123@db:5432/production")
	})

	chain := httpapi.RequestIDMiddleware(a.IDGen())(
		httpapi.RecoveryMiddleware(a.Logger())(panickingHandler),
	)

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()

	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on panic, got: %d", rec.Code)
	}

	body := rec.Body.String()
	if strings.Contains(body, "SuperSecretPass123") {
		t.Fatalf("leak detected: internal secret appeared in 500 response: %s", body)
	}
	if strings.Contains(body, "goroutine") || strings.Contains(body, ".go:") {
		t.Fatalf("leak detected: stack trace appeared in 500 response: %s", body)
	}

	var errResp httpapi.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to unmarshal JSON error response: %v, raw: %s", err, body)
	}
	if errResp.Code != "INTERNAL_ERROR" {
		t.Fatalf("expected code INTERNAL_ERROR, got: %s", errResp.Code)
	}
	if errResp.RequestId == "" {
		t.Fatal("expected request_id in error response")
	}
}
