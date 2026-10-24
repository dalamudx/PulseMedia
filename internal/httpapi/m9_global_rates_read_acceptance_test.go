package httpapi_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

// Freeze only this owned fixture's next collection while measuring GET effects.
// Use the real scheduler lock, so a pre-existing enqueue cannot race the baseline.
func m9FreezeGlobalRateCollection(t *testing.T, f *m5IndexerAcceptance, ids []string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		tx, err := f.app.DBPool().Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err = db.New(tx).LockOutboxAggregate(t.Context(), db.LockOutboxAggregateParams{AggregateType: "download_global_rates_scheduler", AggregateID: "global"}); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		var active int
		if err = tx.QueryRow(t.Context(), `SELECT count(*) FROM download_global_rate_schedule WHERE client_id=ANY($1::uuid[]) AND active_job_id IS NOT NULL`, ids).Scan(&active); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if active == 0 {
			if _, err = tx.Exec(t.Context(), `UPDATE download_global_rate_schedule SET next_due_at=clock_timestamp()+interval '30 minutes' WHERE client_id=ANY($1::uuid[])`, ids); err != nil {
				_ = tx.Rollback(t.Context())
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err = tx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("active native read did not return before HTTP-only baseline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func m9GlobalRateHTTPProtection(t *testing.T, f *m5IndexerAcceptance) string {
	t.Helper()
	var out string
	query := `SELECT jsonb_build_object(
 'attempts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.job_id) FROM download_global_rate_attempt a),
 'schedules',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.client_id) FROM download_global_rate_schedule s),
 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM job j WHERE j.kind='download_global_rates'),
 'clients',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM download_client c),
 'subscriptions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM subscription s),
 'downloads',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM download_task d),
 'file_mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY to_jsonb(m)::text) FROM library_file_unit m),
 'unit_reservations',(SELECT jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text) FROM unit_reservation r),
 'acquisition_reservations',(SELECT jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text) FROM acquisition_reservation r))::text`
	if err := f.app.DBPool().QueryRow(t.Context(), query).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func m9VerifyGlobalRatesHTTP(t *testing.T, f *m5IndexerAcceptance, qbCalls, trCalls *atomic.Int32, ids []string, success bool) {
	t.Helper()
	m9FreezeGlobalRateCollection(t, f, ids)
	before := m9GlobalRateHTTPProtection(t, f)
	beforeQB, beforeTR := qbCalls.Load(), trCalls.Load()
	for range 3 {
		result := f.request(t, "GET", "/api/v1/download-clients/global-rates", "", nil, f.session)
		m5IndexerStatus(t, result, 200)
		if !strings.Contains(result.header.Get("Cache-Control"), "no-store") {
			t.Fatal("global transfer data may be cached")
		}
		value := m5IndexerObject(t, result)
		if value["enabled"] != true || value["freshness_seconds"] != float64(45) {
			t.Fatal("collection mode or time contract missing")
		}
		clients := m5InteractiveRows(t, value, "clients")
		series := m5InteractiveRows(t, value, "series")
		if len(clients) != 2 || len(series) < 1 || len(series) > 62 {
			t.Fatal("global sources or bounded history missing")
		}
		if success {
			if value["download_bytes_per_second"] != "9007199254741010" || value["upload_bytes_per_second"] != "123" || value["download_known_clients"] != float64(2) || value["upload_known_clients"] != float64(2) {
				t.Fatal("HTTP total lost precision, zero or contributors")
			}
			for _, client := range clients {
				client, ok := client.(map[string]any)
				if !ok {
					t.Fatal("native client response is not an object")
				}
				if client["state"] != "observed" {
					t.Fatal("native current observation missing")
				}
			}
		} else {
			if value["download_bytes_per_second"] != nil || value["upload_bytes_per_second"] != nil || value["download_known_clients"] != float64(0) || value["upload_known_clients"] != float64(0) {
				t.Fatal("failed global rates became zero or historical total")
			}
			for _, client := range clients {
				client, ok := client.(map[string]any)
				if !ok {
					t.Fatal("native client response is not an object")
				}
				if client["state"] != "failed" || client["last_good"] == nil || client["observed_at"] == nil {
					t.Fatal("failed client lost failure or historical provenance")
				}
			}
		}
		for _, private := range []string{"owned-global-secret", "owned-reader", "127.0.0.1/owned", "config_stamp", "execution_owner", "lease_token"} {
			if strings.Contains(string(result.body), private) {
				t.Fatal("private executable material entered public rate response")
			}
		}
	}
	m5IndexerStatus(t, f.request(t, "GET", "/api/v1/download-clients/global-rates", "", nil, ""), 401)
	if !success {
		pool := f.app.DBPool()
		if _, err := pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE username='creation-admin'`); err != nil {
			t.Fatal(err)
		}
		denied := f.request(t, "GET", "/api/v1/download-clients/global-rates", "", nil, f.session)
		if _, err := pool.Exec(t.Context(), `UPDATE app_user SET role='admin' WHERE username='creation-admin'`); err != nil {
			t.Fatal(err)
		}
		m5IndexerStatus(t, denied, 403)
		if _, err := pool.Exec(t.Context(), `ALTER TABLE download_global_rate_schedule RENAME TO global_rates_public_unavailable`); err != nil {
			t.Fatal(err)
		}
		unavailable := f.request(t, "GET", "/api/v1/download-clients/global-rates", "", nil, f.session)
		if _, err := pool.Exec(t.Context(), `ALTER TABLE global_rates_public_unavailable RENAME TO download_global_rate_schedule`); err != nil {
			t.Fatal(err)
		}
		m5IndexerStatus(t, unavailable, 503)
		m5IndexerStatus(t, f.request(t, "GET", "/api/v1/download-clients/global-rates", "", nil, f.session), 200)
	}
	if after := m9GlobalRateHTTPProtection(t, f); after != before {
		t.Fatal("GET changed native observations, scheduler, downloaders or protected business rows")
	}
	if qbCalls.Load() != beforeQB || trCalls.Load() != beforeTR {
		t.Fatal("GET made a native downloader request")
	}
	t.Logf("M9_GLOBAL_RATE_HTTP_READ_PASS success=%t Cookie=REAL no_store=true precision=DECIMAL_STRINGS GET_native_calls=0 protected_rows=UNCHANGED", success)
}
