package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestChannelStateHTTPRecovery(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(s *httpapi.ServerHandler, a *app.App) {
		s.SetChannelStateService(notify.NewChannelStateService(postgres.NewChannelStateStore(a.DBPool(), idgen.New())))
		s.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
	})
	ctx := context.Background()
	call := func(token, method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	created := call(admin, "POST", "/api/v1/notification-channels", "state-fixture-create", map[string]any{"name": "State fixture", "channel_type": "webhook", "config": map[string]string{"url": "http://127.0.0.1:9/fixture-private-token", "secret": "state-fixture-private-hmac"}})
	var row httpapi.NotificationChannelDetails
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &row) != nil {
		t.Fatal(created.Code, created.Body.String())
	}
	channel, e := a.Queries().GetNotificationChannelByID(ctx, row.Id)
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/notification-channels/" + row.Id.String() + "/state"
	command := ports.ChannelStateCommand{ExpectedUpdatedAt: row.UpdatedAt, IsEnabled: false}
	if out := call(member, "PUT", path, "state-http", command); out.Code != 403 {
		t.Fatal("member wrote state", out.Code)
	}
	if os.Getenv("PULSE_UI_FIXTURE") == "1" {
		server := httptest.NewServer(router)
		defer server.Close()
		work, cancel := context.WithTimeout(ctx, 95*time.Second)
		defer cancel()
		script, e := filepath.Abs("../../web/tests/channel-state-browser.mjs")
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.CommandContext(work, "node", script)
		cmd.Env = append(os.Environ(), "PULSE_UI_FIXTURE=1", "PULSE_CHANNEL_STATE_UI_BASE="+server.URL, "PULSE_CHANNEL_STATE_UI_ID="+row.Id.String())
		raw, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("channel state browser failed: %v %s", e, raw)
		}
		t.Log(string(raw))
	} else {
		first := call(admin, "PUT", path, "state-http", command)
		if first.Code != 200 {
			t.Fatal(first.Code, first.Body.String())
		}
		again := call(admin, "PUT", path, "state-http", command)
		if again.Code != 200 || again.Body.String() != first.Body.String() {
			t.Fatal("unstable state receipt", again.Code)
		}
		if out := call(admin, "PUT", path, "state-stale", command); out.Code != 409 {
			t.Fatal("old state version accepted", out.Code)
		}
		command.ExpectedUpdatedAt = channel.UpdatedAt.Time
		command.IsEnabled = true
		if out := call(admin, "PUT", path, "state-http", command); out.Code != 409 {
			t.Fatal("same key different fingerprint", out.Code)
		}
	}
	current, e := a.Queries().GetNotificationChannelByID(ctx, row.Id)
	if e != nil || !bytes.Equal(channel.ConfigEncrypted, current.ConfigEncrypted) {
		t.Fatal("state control modified secret destination", e)
	}
	if out := call(member, "PUT", path, "state-http", command); out.Code != 403 {
		t.Fatal("cache elevated member", out.Code)
	}
	var unsafe int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM idempotency_record WHERE response_body::text LIKE '%state-fixture-private%' OR response_body::text LIKE '%fixture-private-token%'").Scan(&unsafe); e != nil || unsafe != 0 {
		t.Fatal("private config cached", unsafe, e)
	}
}
