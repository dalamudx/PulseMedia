package httpapi_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSettingsCommandBrowserHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("owned browser fixture required")
	}
	ctx := context.Background()
	a, _, _ := setupAuthHTTP(t)
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	user, _, e := as.SetupAdmin(ctx, "settings-ui-admin", "SyntheticSettingFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	_, session, e := as.Login(ctx, user.Username, "SyntheticSettingFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	svc := settings.NewService(postgres.NewSettingsStore(a.DBPool(), a.IDGen()), crypto.NewSettingsCipher(make([]byte, 32)))
	handler := httpapi.NewServerHandler(a, as, svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server := httptest.NewServer(httpapi.NewRouter(a, handler, as, nil))
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	script, e := filepath.Abs("../../web/tests/settings-command-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(runCtx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_SETTINGS_BROWSER_BASE="+server.URL, "PULSE_SETTINGS_BROWSER_SESSION="+session, "PULSE_SETTINGS_BROWSER_OWNER="+user.ID.String())
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("settings browser %v: %s", e, raw)
	}
	t.Log(string(raw))
	var version, audits, events, receipts int
	e = a.DBPool().QueryRow(ctx, `SELECT (SELECT version FROM setting WHERE key='tmdb.api_key'),(SELECT count(*) FROM audit_log WHERE action='setting.update'),(SELECT count(*) FROM outbox_event WHERE event_type='setting.updated'),(SELECT count(*) FROM idempotency_record WHERE actor_id=$1)`, user.ID.String()).Scan(&version, &audits, &events, &receipts)
	if e != nil || version != 4 || audits != 4 || events != 4 || receipts != 4 {
		t.Fatalf("duplicate/missing facts: version/audit/event/receipt %d/%d/%d/%d %v", version, audits, events, receipts, e)
	}
	secret, e := svc.GetSecret(ctx, "tmdb.api_key")
	if e != nil || secret != "synthetic-browser-fourth" {
		t.Fatal("encrypted final setting not decryptable", e)
	}
	for _, query := range []string{`SELECT value::text FROM setting`, `SELECT payload::text FROM audit_log WHERE action='setting.update'`, `SELECT payload::text FROM outbox_event WHERE event_type='setting.updated'`, `SELECT response_body::text FROM idempotency_record`} {
		rows, e := a.DBPool().Query(ctx, query)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var text string
			if e = rows.Scan(&text); e != nil {
				t.Fatal(e)
			}
			if strings.Contains(text, "synthetic-browser-") {
				t.Fatal("plaintext entered durable settings/audit/event/receipt")
			}
		}
		rows.Close()
	}
}
