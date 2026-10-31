package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/google/uuid"
)

func TestSystemMetricsRealAuthPG(t *testing.T) {
	a, svc, router := setupAuthHTTP(t)
	ctx := t.Context()
	pool := a.DBPool()
	user, _, err := svc.SetupAdmin(ctx, "metrics-admin", "OwnedMetricsFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	raw := "owned-metrics-" + uuid.NewString()
	id := uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'metrics',$3,'["read"]')`, id, user.ID, crypto.HashToken(raw)); err != nil {
		t.Fatal(err)
	}
	counts := func() [5]int64 {
		var result [5]int64
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM job),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM outbox_delivery),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM idempotency_record)`).Scan(&result[0], &result[1], &result[2], &result[3], &result[4]); err != nil {
			t.Fatal(err)
		}
		return result
	}
	request := func(token string, want int) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/system/metrics?cookie=private-query", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("status=%d cache=%s body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
		if strings.Contains(w.Body.String(), raw) || strings.Contains(w.Body.String(), "private-query") {
			t.Fatal("credentials exposed")
		}
		return w
	}
	request("", http.StatusUnauthorized)
	before := counts()
	body := request(raw, http.StatusOK).Body.String()
	if !strings.Contains(body, "pulse_db_lock_waiters 0\n") || !strings.Contains(body, "pulse_db_lock_wait_age_seconds 0\n") {
		t.Fatal("lock collector not wired")
	}
	if !strings.Contains(body, "pulse_db_pool_max_connections ") || !strings.Contains(body, "pulse_queue_tasks{queue=\"job\",state=\"queued\"}") {
		t.Fatal("production collector not wired")
	}
	body = request(raw, http.StatusOK).Body.String()
	if !strings.Contains(body, "pulse_http_requests_total{route=\"GET /api/v1/system/metrics\",method=\"GET\",status=\"200\"} 1\n") {
		t.Fatal("request observations not connected")
	}
	if !reflect.DeepEqual(before, counts()) {
		t.Fatal("scrape wrote business rows")
	}
	if _, err = pool.Exec(ctx, `UPDATE api_token SET scopes='[]' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	request(raw, http.StatusForbidden)
	if _, err = pool.Exec(ctx, `UPDATE api_token SET scopes='["read"]' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	request(raw, http.StatusForbidden)
	if _, err = pool.Exec(ctx, `UPDATE app_user SET role='admin' WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE api_token SET revoked_at=statement_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	request(raw, http.StatusUnauthorized)
}
