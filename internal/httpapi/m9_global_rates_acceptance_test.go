package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
)

func TestM9GlobalRatesNativeAppCookiePG(t *testing.T) {
	f, _ := newM5CreationAcceptance(t, func(c *config.Config) { c.DownloadGlobalRates.Enabled = true })
	var qbCalls, trCalls, qbFailure, trFailure atomic.Int32
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "owned-global-session", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/transfer/info":
			if r.Method != "GET" {
				t.Error("global qB observation was not GET")
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			cookie, err := r.Cookie("SID")
			if err != nil || cookie.Value != "owned-global-session" {
				http.Error(w, "login", http.StatusForbidden)
				return
			}
			qbCalls.Add(1)
			if code := qbFailure.Load(); code != 0 {
				http.Error(w, "owned rejected observation", int(code))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"dl_info_speed": int64(9007199254740993), "up_info_speed": int64(123), "dl_info_data": int64(1), "up_info_data": int64(1)})
		default:
			t.Error("qB called a non-global endpoint")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(qb.Close)
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Error("Transmission RPC was not POST")
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "owned-global-session" {
			w.Header().Set("X-Transmission-Session-Id", "owned-global-session")
			w.WriteHeader(409)
			return
		}
		var request struct {
			Method    string                     `json:"method"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != "session-stats" || len(request.Arguments) != 0 {
			t.Error("Transmission called a non-global or mutating method")
			http.Error(w, "method", http.StatusBadRequest)
			return
		}
		trCalls.Add(1)
		if code := trFailure.Load(); code != 0 {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "owned rate limit", int(code))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"downloadSpeed": int64(17), "uploadSpeed": int64(0)}})
	}))
	t.Cleanup(tr.Close)
	// Stop and join App before its source servers close, including failure paths.
	t.Cleanup(f.stopRuntime)
	ids := make([]string, 0, 2)
	for _, item := range []struct{ name, kind, url string }{{"owned native qB", "qbittorrent", qb.URL}, {"owned native Transmission", "transmission", tr.URL}} {
		result := f.request(t, "POST", "/api/v1/download-clients", "", map[string]any{"name": item.name, "client_type": item.kind, "url": item.url, "username": "owned-reader", "password": "owned-global-secret", "remote_path_map": map[string]string{}}, f.session)
		m5IndexerStatus(t, result, 201)
		ids = append(ids, m5CreationID(t, result, "id").String())
	}
	pool := f.app.DBPool()
	wait := func(label string, query string, args ...any) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for {
			var n int
			if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
				t.Fatal(label, err)
			}
			if n == 2 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s expected both clients; matched=%d qb_calls=%d tr_calls=%d", label, n, qbCalls.Load(), trCalls.Load())
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	wait("native global samples", `SELECT count(*) FROM download_global_rate_schedule s JOIN download_global_rate_attempt a ON a.job_id=s.latest_job_id WHERE s.client_id=ANY($1::uuid[]) AND a.state='succeeded' AND ((s.client_id=$2::uuid AND a.download_bytes_per_second=9007199254740993 AND a.upload_bytes_per_second=123) OR (s.client_id=$3::uuid AND a.download_bytes_per_second=17 AND a.upload_bytes_per_second=0))`, ids, ids[0], ids[1])
	if qbCalls.Load() < 1 || trCalls.Load() < 1 {
		t.Fatal("samples did not originate from both native clients")
	}
	m9VerifyGlobalRatesHTTP(t, f, &qbCalls, &trCalls, ids, true)
	qbFailure.Store(403)
	trFailure.Store(429)
	if _, err := pool.Exec(t.Context(), `UPDATE download_global_rate_schedule SET next_due_at=clock_timestamp()-interval '1 second' WHERE client_id=ANY($1::uuid[])`, ids); err != nil {
		t.Fatal(err)
	}
	wait("safe failure with last-good values", `SELECT count(*) FROM download_global_rate_schedule s JOIN download_global_rate_attempt a ON a.job_id=s.latest_job_id JOIN download_global_rate_attempt good ON good.job_id=s.last_good_job_id WHERE s.client_id=ANY($1::uuid[]) AND a.state='failed' AND a.download_bytes_per_second IS NULL AND a.upload_bytes_per_second IS NULL AND good.state='succeeded' AND good.observed_at<a.observed_at AND ((s.client_id=$2::uuid AND a.failure_code='auth_required' AND good.download_bytes_per_second=9007199254740993) OR (s.client_id=$3::uuid AND a.failure_code='rate_limited' AND good.download_bytes_per_second=17 AND s.next_due_at>clock_timestamp()+interval '40 seconds'))`, ids, ids[0], ids[1])
	m9VerifyGlobalRatesHTTP(t, f, &qbCalls, &trCalls, ids, false)
	for _, table := range []string{"subscription", "download_task", "library_file_unit", "unit_reservation", "acquisition_reservation"} {
		var count int
		if err := pool.QueryRow(t.Context(), fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("global statistics created unrelated business rows", table, count)
		}
	}
	t.Log("M9_GLOBAL_RATE_NATIVE_APP_PASS Cookie=REAL App=REAL PG=18.6 protocols=qB_Transmission rate_source=GLOBAL_NATIVE precision=int64 idle_zero=KNOWN failures=403_429 last_good_preserved=true download_side_effects=0")
}
