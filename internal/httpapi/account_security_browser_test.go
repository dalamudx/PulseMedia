package httpapi_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAccountSecurityActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("owned browser fixture required")
	}
	a, _, _ := setupAuthHTTP(t)
	ctx := t.Context()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	as.SetMFACipher(cipher)
	_, session, e := as.SetupAdmin(ctx, "security-ui-admin", "SecurityBrowserFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	store := postgres.NewAccountManagementStore(a.DBPool(), a.IDGen())
	h := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAccountManagementService(auth.NewAccountManagementService(store, crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen(), cipher))
	h.SetMFAService(auth.NewMFAService(store, crypto.AuthenticationSecrets{}, cipher, a.Clock(), a.IDGen()))
	h.SetTokenManagementService(auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, a.Clock()))
	router := httpapi.NewRouter(a, h, as, nil)
	dist := os.Getenv("PULSE_ACCOUNT_SECURITY_DIST")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/__test/advance-security-clock" {
			a.Clock().(*clock.MockClock).Advance(time.Minute)
			w.Header().Set("X-Fixture-Time", strconv.FormatInt(a.Clock().Now().UnixMilli(), 10))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if dist != "" && !strings.HasPrefix(r.URL.Path, "/api/") && r.Method == "GET" {
			name := strings.TrimPrefix(r.URL.Path, "/")
			if name == "" || !strings.Contains(filepath.Base(name), ".") {
				http.ServeFile(w, r, filepath.Join(dist, "index.html"))
				return
			}
			http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
			return
		}
		router.ServeHTTP(w, r)
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	script, e := filepath.Abs("../../web/tests/account-security-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	// Chromium Unix sockets need a shorter path than the nested PG build directory.
	browserTemp, e := os.MkdirTemp("/data/temp", "pulse-security-chrome-")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = os.RemoveAll(browserTemp) }()
	command := exec.CommandContext(browserCtx, "node", script)
	command.Env = append(os.Environ(), "TMPDIR="+browserTemp, "TMP="+browserTemp, "TEMP="+browserTemp, "PULSE_ACCOUNT_SECURITY_BASE="+server.URL, "PULSE_ACCOUNT_SECURITY_SESSION="+session, "PULSE_ACCOUNT_SECURITY_TIME="+strconv.FormatInt(a.Clock().Now().UnixMilli(), 10))
	output, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("account security browser failed: %v: %s", e, output)
	}
	t.Log(string(output))
	var users, enabled int
	if e = a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM app_user),(SELECT count(*) FROM account_totp WHERE enabled)").Scan(&users, &enabled); e != nil || users != 3 || enabled != 1 {
		t.Fatalf("browser final business state accounts=%d enabled_mfa=%d error=%v", users, enabled, e)
	}
}
