package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotificationConfigurationAdminEncryptedAndNeverReturned(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t)
	body := `{"name":"Webhook","channel_type":"webhook","config":{"url":"https://example.test/private-destination?token=private-query-token","secret":"private-hmac-secret"}}`
	send := func(method, path, token, data string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "config-key-must-not-cache-secrets")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := send(method, "/api/v1/notification-channels", member, body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("member could configure or read private channels: %d", w.Code)
		}
	}
	if w := send(http.MethodPost, "/api/v1/notification-channels", admin, `{"name":"Invalid","channel_type":"webhook","config":{"url":"file:///private.env"}}`); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
	created := send(http.MethodPost, "/api/v1/notification-channels", admin, body)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "private-") || strings.Contains(created.Body.String(), "config") {
		t.Fatal("private config returned", created.Body.String())
	}
	var details httpapi.NotificationChannelDetails
	if err := json.Unmarshal(created.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	results := make(chan *httptest.ResponseRecorder, 10)
	for i := 0; i < 10; i++ {
		go func() { results <- send(http.MethodPost, "/api/v1/notification-channels", admin, body) }()
	}
	for i := 0; i < 10; i++ {
		replayed := <-results
		var got httpapi.NotificationChannelDetails
		if replayed.Code != http.StatusCreated {
			t.Fatal(replayed.Code, replayed.Body.String())
		}
		if err := json.Unmarshal(replayed.Body.Bytes(), &got); err != nil || got.Id != details.Id {
			t.Fatal("duplicate channel on response-loss replay", err)
		}
	}
	if conflict := send(http.MethodPost, "/api/v1/notification-channels", admin, strings.ReplaceAll(body, "private-hmac-secret", "private-other-secret")); conflict.Code != http.StatusConflict {
		t.Fatal("same key ignored different config", conflict.Code)
	}
	var channels, audits int
	if err := a.DBPool().QueryRow(context.Background(), "SELECT (SELECT count(*) FROM notification_channel),(SELECT count(*) FROM audit_log WHERE action='notification_channel.created')").Scan(&channels, &audits); err != nil || channels != 1 || audits != 1 {
		t.Fatal("duplicate transactional config/audit", channels, audits, err)
	}
	row, err := a.Queries().GetNotificationChannelByID(context.Background(), details.Id)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.ConfigEncrypted, []byte("private-")) || string(row.Config) != "{}" {
		t.Fatal("plaintext integration configuration in PG")
	}
	plain, err := crypto.Decrypt(make([]byte, 32), row.ConfigEncrypted)
	if err != nil || !strings.Contains(string(plain), "private-hmac-secret") {
		t.Fatal("channel encryption uses inconsistent key", err)
	}
	listed := send(http.MethodGet, "/api/v1/notification-channels", admin, "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "private-") || strings.Contains(listed.Body.String(), "config") {
		t.Fatal(listed.Code, listed.Body.String())
	}
	var leaked bool
	if err := a.DBPool().QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM audit_log WHERE payload::text LIKE '%private-%') OR EXISTS (SELECT 1 FROM idempotency_record WHERE response_body::text LIKE '%private-%')").Scan(&leaked); err != nil || leaked {
		t.Fatal("config leaked in audit/cache", err)
	}
	path := "/api/v1/notification-channels/" + details.Id.String()
	if w := send(http.MethodDelete, path, member, ""); w.Code != http.StatusForbidden {
		t.Fatal("member could disable channel", w.Code)
	}
	if w := send(http.MethodDelete, path, admin, ""); w.Code != http.StatusConflict {
		t.Fatal(w.Code, w.Body.String())
	}
	disabled, err := a.Queries().GetNotificationChannelByID(context.Background(), details.Id)
	if err != nil || !disabled.IsEnabled {
		t.Fatal("retired unfenced command changed state", err)
	}
}
