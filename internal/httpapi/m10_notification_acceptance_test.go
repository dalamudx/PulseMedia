package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

func robotHTTPFixture(t *testing.T) (*app.App, string, string, http.Handler) {
	t.Helper()
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(h *httpapi.ServerHandler, a *app.App) {
		h.SetNotificationTestService(notify.NewTestService(postgres.NewNotificationTestStore(a.DBPool(), idgen.New(), outbox.New(a.DBPool(), idgen.New(), clock.New()))))
		h.SetChannelStateService(notify.NewChannelStateService(postgres.NewChannelStateStore(a.DBPool(), idgen.New())))
		h.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
	})
	return a, admin, member, router
}
func robotCookieRequest(t *testing.T, router http.Handler, token, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: token})
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("cacheable response", w.Code)
	}
	if strings.Contains(w.Body.String(), "owned-private") || strings.Contains(w.Body.String(), "SQLSTATE") {
		t.Fatal("credentials or SQL leaked")
	}
	return w
}
func TestM10NotificationCookieConfigurationAndOriginalPG(t *testing.T) {
	a, admin, member, router := robotHTTPFixture(t)
	destinations := map[string]string{"wechat": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=owned-private-key", "dingtalk": "https://oapi.dingtalk.com/robot/send?access_token=owned-private-token", "feishu": "https://open.feishu.cn/open-apis/bot/v2/hook/owned-private-hook", "bark": "https://push.example/owned-private-key"}
	var first httpapi.NotificationChannelDetails
	for kind, destination := range destinations {
		raw, _ := json.Marshal(map[string]any{"name": "owned " + kind, "channel_type": kind, "config": map[string]string{"url": destination}})
		w := robotCookieRequest(t, router, admin, "POST", "/api/v1/notification-channels", "create-"+kind, string(raw))
		if w.Code != 201 {
			t.Fatal(kind, w.Code, w.Body.String())
		}
		var channel httpapi.NotificationChannelDetails
		if json.Unmarshal(w.Body.Bytes(), &channel) != nil || channel.Id == uuid.Nil {
			t.Fatal("missing channel")
		}
		if first.Id == uuid.Nil {
			first = channel
		}
		replay := robotCookieRequest(t, router, admin, "POST", "/api/v1/notification-channels", "create-"+kind, string(raw))
		if replay.Code != 201 || !bytes.Equal(w.Body.Bytes(), replay.Body.Bytes()) {
			t.Fatal("original channel not restored")
		}
		var encrypted []byte
		if e := a.DBPool().QueryRow(t.Context(), `SELECT config_encrypted FROM notification_channel WHERE id=$1`, channel.Id).Scan(&encrypted); e != nil || bytes.Contains(encrypted, []byte("owned-private")) {
			t.Fatal("unencrypted destination", e)
		}
	}
	path := "/api/v1/notification-channels/" + first.Id.String() + "/test"
	raw := fmt.Sprintf(`{"expected_updated_at":%q}`, first.UpdatedAt.Format(time.RFC3339Nano))
	// Same original command under concurrency queues exactly one immutable event/delivery.
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- robotCookieRequest(t, router, admin, "POST", path, "owned-test-key", raw)
		}()
	}
	wg.Wait()
	close(results)
	var original []byte
	for w := range results {
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		if original == nil {
			original = append([]byte(nil), w.Body.Bytes()...)
		} else if !bytes.Equal(original, w.Body.Bytes()) {
			t.Fatal("different receipt")
		}
	}
	var receipt ports.NotificationTestReceipt
	if json.Unmarshal(original, &receipt) != nil || receipt.Status != "queued" || receipt.ChannelID != first.Id {
		t.Fatal("not a queued receipt")
	}
	var deliveries int
	if e := a.DBPool().QueryRow(t.Context(), `SELECT count(*) FROM outbox_delivery WHERE event_id=$1 AND state='pending'`, receipt.EventID).Scan(&deliveries); e != nil || deliveries != 1 {
		t.Fatal("duplicate test", deliveries, e)
	}
	observed := robotCookieRequest(t, router, admin, "GET", "/api/v1/integrations/deliveries", "", "")
	if observed.Code != 200 || !bytes.Contains(observed.Body.Bytes(), []byte(`"duplicate_risk":"robot_may_duplicate"`)) {
		t.Fatal("robot replay risk missing", observed.Code, observed.Body.String())
	}
	if w := robotCookieRequest(t, router, member, "POST", path, "member-test", raw); w.Code != 403 {
		t.Fatal("member queued")
	}
	disabled := fmt.Sprintf(`{"expected_updated_at":%q,"is_enabled":false}`, first.UpdatedAt.Format(time.RFC3339Nano))
	if w := robotCookieRequest(t, router, admin, "PUT", "/api/v1/notification-channels/"+first.Id.String()+"/state", "disable", disabled); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := robotCookieRequest(t, router, admin, "POST", path, "owned-test-key", raw); w.Code != 202 || !bytes.Equal(w.Body.Bytes(), original) {
		t.Fatal("disabled original lost")
	}
	if w := robotCookieRequest(t, router, admin, "POST", path, "new-disabled-test", raw); w.Code != 409 {
		t.Fatal("disabled new test queued")
	}
	for _, bad := range []string{`{"name":"x","channel_type":"wechat","config":{"url":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x","URL":"different"}}`, `{"name":"x","channel_type":"bark","config":{"url":"https://push.example/key","url":"https://evil.example/key"}}`, `{"Name":"x","channel_type":"bark","config":{}}`, `{"name":"x","channel_type":"bark","config":null}`} {
		if w := robotCookieRequest(t, router, admin, "POST", "/api/v1/notification-channels", "bad-json", bad); w.Code != 400 {
			t.Fatal("invalid JSON accepted", w.Code)
		}
	}
	// Resolve the actual fixture administrator from the test command's audit.
	if _, e := a.DBPool().Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id::text=(SELECT actor_id FROM audit_log WHERE action='notification.test.queued' LIMIT 1)`); e != nil {
		t.Fatal(e)
	}
	if w := robotCookieRequest(t, router, admin, "POST", path, "owned-test-key", raw); w.Code != 403 {
		t.Fatal("historical receipt bypassed revocation", w.Code)
	}
	t.Log("M10_NOTIFICATION_COOKIE_PASS four_channels=ENCRYPTED test=QUEUED concurrent=ONE original=STABLE permission=LIVE JSON=STRICT network=NONE")
}
func TestM10NotificationTestTailRollbackPG(t *testing.T) {
	a, admin, _, router := robotHTTPFixture(t)
	w := robotCookieRequest(t, router, admin, "POST", "/api/v1/notification-channels", "bark-rollback", `{"name":"owned","channel_type":"bark","config":{"url":"https://push.example/owned-private"}}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var channel httpapi.NotificationChannelDetails
	_ = json.Unmarshal(w.Body.Bytes(), &channel)
	path := "/api/v1/notification-channels/" + channel.Id.String() + "/test"
	raw := fmt.Sprintf(`{"expected_updated_at":%q}`, channel.UpdatedAt.Format(time.RFC3339Nano))
	for _, table := range []string{"outbox_event", "outbox_delivery", "audit_log", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			var before string
			if e := a.DBPool().QueryRow(t.Context(), `SELECT md5(jsonb_build_array((SELECT jsonb_agg(x) FROM outbox_event x),(SELECT jsonb_agg(x) FROM outbox_delivery x),(SELECT jsonb_agg(x) FROM audit_log x),(SELECT jsonb_agg(x) FROM idempotency_record x))::text)`).Scan(&before); e != nil {
				t.Fatal(e)
			}
			if _, e := a.DBPool().Exec(t.Context(), `CREATE FUNCTION owned_notification_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'owned-private-fault';END$$; CREATE TRIGGER owned_notification_tail BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION owned_notification_tail()`); e != nil {
				t.Fatal(e)
			}
			defer func() {
				_, _ = a.DBPool().Exec(context.Background(), `DROP TRIGGER IF EXISTS owned_notification_tail ON `+table+`; DROP FUNCTION IF EXISTS owned_notification_tail()`)
			}()
			w := robotCookieRequest(t, router, admin, "POST", path, "test-tail-"+table, raw)
			if w.Code != 503 {
				t.Fatal(w.Code, w.Body.String())
			}
			var after string
			if e := a.DBPool().QueryRow(t.Context(), `SELECT md5(jsonb_build_array((SELECT jsonb_agg(x) FROM outbox_event x),(SELECT jsonb_agg(x) FROM outbox_delivery x),(SELECT jsonb_agg(x) FROM audit_log x),(SELECT jsonb_agg(x) FROM idempotency_record x))::text)`).Scan(&after); e != nil || after != before {
				t.Fatal("partial test queue ledger", e)
			}
		})
	}
	t.Log("M10_NOTIFICATION_TEST_TAIL_PASS event_delivery_audit_receipt=ATOMIC native_IO=NONE")
}
