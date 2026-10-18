package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRSSAdminConfigurationEncryptedAndReplaySafe(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	var id string
	if e := a.DBPool().QueryRow(ctx, "SELECT id::text FROM indexer LIMIT 1").Scan(&id); e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/indexers/" + id + "/rss"
	body := `{"enabled":true,"kind":"rss","interval_seconds":30,"expected_version":1,"feed_url":"https://fixture.invalid/rss?passkey=private-feed-passkey"}`
	send := func(method, token, data, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if w := send(method, member, body, "feed-key"); w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := send(http.MethodPut, admin, body, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	first := send(http.MethodPut, admin, body, "feed-key")
	if first.Code != 200 || strings.Contains(first.Body.String(), "private-feed-passkey") {
		t.Fatal(first.Code, first.Body.String())
	}
	if second := send(http.MethodPut, admin, body, "feed-key"); second.Code != 200 || second.Body.String() != first.Body.String() {
		t.Fatal(second.Code, second.Body.String())
	}
	if w := send(http.MethodPut, admin, strings.Replace(body, "private-feed-passkey", "changed", 1), "feed-key"); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := send(http.MethodPut, admin, body, "new-key"); w.Code != 409 {
		t.Fatal(w.Code)
	}
	get := send(http.MethodGet, admin, "", "")
	var info map[string]any
	if get.Code != 200 || json.Unmarshal(get.Body.Bytes(), &info) != nil || info["config_version"] != float64(2) || strings.Contains(get.Body.String(), "fixture.invalid") {
		t.Fatal(get.Code, get.Body.String())
	}
	var raw []byte
	if e := a.DBPool().QueryRow(ctx, "SELECT rss_url_encrypted FROM indexer WHERE id=$1", id).Scan(&raw); e != nil || bytes.Contains(raw, []byte("private-feed-passkey")) {
		t.Fatal(e)
	}
	var leaks int
	if e := a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_log WHERE payload::text LIKE '%private-feed-passkey%')+(SELECT count(*) FROM outbox_event WHERE payload::text LIKE '%private-feed-passkey%')+(SELECT count(*) FROM idempotency_record WHERE response_body::text LIKE '%private-feed-passkey%')").Scan(&leaks); e != nil || leaks != 0 {
		t.Fatal(leaks, e)
	}
	if w := send(http.MethodPut, admin, strings.Replace(strings.Replace(body, `"expected_version":1`, `"expected_version":2`, 1), "https://fixture.invalid/rss?passkey=private-feed-passkey", "file:///protected.env", 1), "invalid"); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}
