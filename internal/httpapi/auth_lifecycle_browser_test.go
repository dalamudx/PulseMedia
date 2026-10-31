package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

func TestAuthLifecycleBrowserHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("owned browser fixture required")
	}
	t.Setenv("PULSE_SETUP_TOKEN", "synthetic-auth-ui-deployment-token")
	t.Setenv("PULSE_AUTH_UI_SETUP_TOKEN", "synthetic-auth-ui-deployment-token")
	a, _, _ := setupAuthHTTP(t)
	service := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	handler := httpapi.NewServerHandler(a, service, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetTokenManagementService(auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, a.Clock()))
	testRouter := httpapi.NewRouter(a, handler, service, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/owned-auth-fixture/expire" && r.Method == http.MethodPost {
			if _, err := a.DBPool().Exec(r.Context(), "UPDATE app_session SET expires_at=clock_timestamp()-interval '1 second'"); err != nil {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		}
		testRouter.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	repo := os.Getenv("PULSE_TOKEN_UI_REPO")
	if repo == "" {
		t.Fatal("owned repository path required")
	}
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "web/tests/auth-lifecycle-browser.mjs"))
	command.Env = append(os.Environ(), "PULSE_AUTH_UI_BASE="+server.URL)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("auth lifecycle browser: %v: %s", err, output)
	}
	t.Log(string(output))
	var users int
	if err = a.DBPool().QueryRow(context.Background(), "SELECT count(*) FROM app_user").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("unexpected user count %d", users)
	}
}

func TestAuthStatusDatabaseFailureHTTPPG(t *testing.T) {
	a, service, router := setupAuthHTTP(t)
	if _, _, err := service.SetupAdmin(context.Background(), "status-admin", "StatusNativeFixture2026!"); err != nil {
		t.Fatal(err)
	}
	a.DBPool().Close()
	request := httptest.NewRequest("GET", "/api/v1/auth/status", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != 500 {
		t.Fatalf("database failure advertised successful status: %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "initialized") || strings.Contains(recorder.Body.String(), "closed pool") {
		t.Fatal("failure exposed a successful initialization state or internal cause")
	}
}
