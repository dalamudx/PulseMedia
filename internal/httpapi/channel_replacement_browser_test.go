package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestChannelReplacementBrowser(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit browser fixture only")
	}
	a, admin, _, _, _, _, router, _ := setupOperationHTTP(t, func(s *httpapi.ServerHandler, a *app.App) {
		s.SetChannelReplacementService(notify.NewChannelReplacementService(postgres.NewChannelReplacementStore(a.DBPool(), idgen.New()), crypto.NewSecretProtector(make([]byte, 32))))
		s.SetChannelStateService(notify.NewChannelStateService(postgres.NewChannelStateStore(a.DBPool(), idgen.New())))
		s.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
		s.SetBatchQueryService(notify.NewBatchQueryService(postgres.NewNotificationBatchQueryStore(a.DBPool())))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	ids := []uuid.UUID{}
	for _, name := range []string{"Rotate Hook", "Conflict Hook", "Refresh Hook"} {
		raw, _ := json.Marshal(map[string]any{"name": name, "channel_type": "webhook", "config": map[string]string{"url": "https://old.example.test/private-ui-path", "secret": "private-old-ui-hmac"}})
		r := httptest.NewRequest("POST", "/api/v1/notification-channels", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+admin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "create-"+name)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var d httpapi.NotificationChannelDetails
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &d) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		ids = append(ids, d.Id)
	}
	ob := outbox.New(a.DBPool(), idgen.New(), clock.New())
	tx, e := a.DBPool().Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	event, e := ob.Publish(ctx, a.Queries().WithTx(tx), "fixture", "old-only", "notification.fixture", map[string]string{"fixture": "not sent"}, []string{"notify:" + ids[0].String()})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	script, e := filepath.Abs("../../web/tests/channel-replacement-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_UI_FIXTURE=1", "PULSE_REPLACE_UI_BASE="+server.URL, "PULSE_REPLACE_UI_OLD_ID="+ids[0].String(), "PULSE_REPLACE_UI_CONFLICT_ID="+ids[1].String(), "PULSE_REPLACE_UI_REFRESH_ID="+ids[2].String())
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("replacement browser failed: %v %s", e, raw)
	}
	t.Log(string(raw))
	old, e := a.Queries().GetNotificationChannelByID(ctx, ids[0])
	if e != nil || old.IsEnabled || !old.ReplacedBy.Valid {
		t.Fatal("UI did not persist replacement", e)
	}
	var count int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM outbox_delivery WHERE event_id=$1 AND consumer=$2 AND state='pending' AND attempts=0", event.ID, "notify:"+ids[0].String()).Scan(&count); e != nil || count != 1 {
		t.Fatal("UI moved/delivered old event", count, e)
	}
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM notification_channel").Scan(&count); e != nil || count != 5 {
		t.Fatal("response-loss duplicated channel", count, e)
	}
}
