package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExpiredTokenOriginalReceiptRecoveryHTTPPG(t *testing.T) {
	ctx := context.Background()
	a, _, _ := setupAuthHTTP(t)
	var databaseNow time.Time
	if err := a.DBPool().QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewMock(databaseNow)
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, clk, a.IDGen())
	_, session, err := as.SetupAdmin(ctx, "expired-receipt-admin", "ExpiredReceiptNativeFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetTokenManagementService(auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, clk))
	router := httpapi.NewRouter(a, h, as, nil)
	request := func(credential, path, key, body string) *httptest.ResponseRecorder {
		method := "GET"
		if body != "" {
			method = "POST"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	expiry := databaseNow.Add(time.Minute)
	body := `{"name":"short lifetime","expires_at":"` + expiry.UTC().Format(time.RFC3339Nano) + `"}`
	first := request(session, "/api/v1/auth/tokens", "expiry-original", body)
	var original struct {
		ports.TokenCommandResult
		Secret string `json:"secret"`
	}
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &original) != nil || original.Secret == "" || original.Replayed {
		t.Fatal("initial creation failed")
	}
	clk.Set(expiry.Add(time.Second))
	if w := request(original.Secret, "/api/v1/auth/me", "", ""); w.Code != 401 {
		t.Fatal("expired token accepted")
	}
	recovered := request(session, "/api/v1/auth/tokens", "expiry-original", body)
	var restored struct {
		ports.TokenCommandResult
		Secret string `json:"secret"`
	}
	if recovered.Code != 200 || json.Unmarshal(recovered.Body.Bytes(), &restored) != nil || !restored.Replayed || restored.Secret != "" || restored.Item.ID != original.Item.ID || restored.Item.ExpiresAt == nil || restored.Item.ExpiresAt.After(clk.Now()) || strings.Contains(recovered.Body.String(), original.Secret) {
		t.Fatal("expired original receipt not recovered safely")
	}
	if w := request(session, "/api/v1/auth/tokens", "expired-new-command", body); w.Code != 400 {
		t.Fatal("expired new command accepted")
	}
	var tokens, audits, events, receipts int
	if err = a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM api_token),(SELECT count(*) FROM audit_log WHERE action='auth.token.created'),(SELECT count(*) FROM outbox_event WHERE event_type='auth.token.created'),(SELECT count(*) FROM idempotency_record)`).Scan(&tokens, &audits, &events, &receipts); err != nil || tokens != 1 || audits != 1 || events != 1 || receipts != 1 {
		t.Fatal("expiry recovery or rejection wrote duplicate state")
	}
}
