package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testNativeTorznabCapabilitiesHealth(t *testing.T, manual bool) {
	f := newMovieLinkFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	const secret = "synthetic-health-caps-key"
	var mode atomic.Int32
	var capsCalls, searchCalls atomic.Int32
	var capsAt atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != secret {
			t.Error("missing configured credential")
		}
		if r.URL.Query().Get("t") == "caps" {
			capsCalls.Add(1)
			capsAt.Store(time.Now().UnixNano())
			switch mode.Load() {
			case 1:
				_, _ = fmt.Fprint(w, `<error code="100" description="apikey=synthetic-health-caps-key"/>`)
				return
			case 2:
				_, _ = fmt.Fprint(w, `<rss><channel/></rss>`)
				return
			case 3:
				_, _ = fmt.Fprint(w, `<caps><searching><movie-search available="no"/></searching></caps>`)
				return
			case 4:
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(429)
				return
			case 5:
				w.WriteHeader(503)
				return
			}
			_, _ = fmt.Fprint(w, `<caps><limits default="25" max="100"/><searching><movie-search available="yes" supportedParams="q,imdbid"/></searching></caps>`)
			return
		}
		searchCalls.Add(1)
		if r.URL.Query().Get("t") != "movie" || r.URL.Query().Get("q") != "" || r.URL.Query().Get("limit") != "1" {
			t.Error("unbounded health credential proof")
		}
		if time.Now().UnixNano()-capsAt.Load() < int64(30*time.Millisecond) {
			t.Error("second request bypassed interval")
		}
		if mode.Load() == 6 {
			w.WriteHeader(401)
			return
		}
		_, _ = fmt.Fprint(w, `<rss><channel><item><title>Probe.Result.1999.1080p.BluRay</title><guid>must-not-persist</guid><enclosure url="https://fixture.invalid/dl?passkey=synthetic-health-caps-key" length="1000"/></item></channel></rss>`)
	}))
	defer server.Close()
	ids := idgen.New()
	key := bytes.Repeat([]byte{7}, 32)
	cipher, e := crypto.Encrypt(key, []byte(secret))
	if e != nil {
		t.Fatal(e)
	}
	q := postgres.Queries(f.pool)
	svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), ids, crypto.NewSecretProtector(key), 30*time.Millisecond)
	repo := postgres.NewIndexerHealthStore(f.pool, ids)
	svc.SetHealthRepository(repo)
	health := search.NewHealthService(repo, svc)
	queue := jobs.NewQueue(f.pool, ids, clock.New())
	for _, tt := range []struct {
		name, reason string
		mode         int32
		wantSearch   int32
	}{
		{"caps_auth", "unavailable", 1, 0}, {"rss_not_caps", "unavailable", 2, 0}, {"unsupported", "unavailable", 3, 0}, {"rate_limit", "rate_limited", 4, 0}, {"service_failure", "unavailable", 5, 0}, {"public_caps_bad_key", "unavailable", 6, 1}, {"credential_ok", "ok", 0, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mode.Store(tt.mode)
			capsCalls.Store(0)
			searchCalls.Store(0)
			idx, e := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: ids.NewUUID(), Name: tt.name, Url: server.URL, ApiKeyEncrypted: cipher, Categories: []byte("[]")})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE indexer SET rss_enabled=true,health='offline',health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id=$1", idx.ID); e != nil {
				t.Fatal(e)
			}
			svc.RegisterClient(idx.ID, torznab.NewClient(server.URL, secret, server.Client()))
			if manual {
				ok, _, _, err := svc.TestIndexer(ctx, idx.ID)
				if ok != (tt.reason == "ok") || (err == nil) != ok {
					t.Fatal("incorrect manual probe result", ok, err)
				}
			} else {
				if n, e := health.QueueDue(ctx, 20); e != nil || n != 1 {
					t.Fatal(n, e)
				}
				j, e := queue.Claim(ctx, []string{"indexer_health"}, "caps-owner", 45*time.Second)
				if e != nil || j == nil {
					t.Fatal(j, e)
				}
				var work ports.IndexerHealthJob
				if e = json.Unmarshal(j.Payload, &work); e != nil {
					t.Fatal(e)
				}
				work.ID, work.Owner, work.Token = j.ID, "caps-owner", j.LeaseToken
				if e = health.ExecuteJob(ctx, work); e != nil {
					t.Fatal(e)
				}
			}
			row, e := q.GetIndexerByID(ctx, idx.ID)
			if e != nil || row.HealthReason != tt.reason || ((tt.reason == "ok") != (row.Health == "online")) {
				t.Fatal("incorrect health observation", row.Health, row.HealthReason, e)
			}
			if capsCalls.Load() != 1 || searchCalls.Load() != tt.wantSearch {
				t.Fatal("unexpected request counts", capsCalls.Load(), searchCalls.Load())
			}
			var queryCount, candidateCount, auditCount, eventCount int
			var raw string
			if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM indexer_query WHERE indexer_id=$1),(SELECT count(*) FROM release_candidate WHERE indexer_id=$1),(SELECT count(*) FROM audit_log WHERE action='indexer.health_observed' AND target_id=$1::text),(SELECT count(*) FROM outbox_event WHERE event_type='indexer.health_observed' AND aggregate_id=$1::text),COALESCE((SELECT error FROM indexer_query WHERE indexer_id=$1 LIMIT 1),'')`, idx.ID).Scan(&queryCount, &candidateCount, &auditCount, &eventCount, &raw); e != nil {
				t.Fatal(e)
			}
			if queryCount != 1 || candidateCount != 0 || auditCount != 1 || eventCount != 1 {
				t.Fatal("non-atomic or candidate-producing probe", queryCount, candidateCount, auditCount, eventCount)
			}
			if bytes.Contains([]byte(raw), []byte(secret)) {
				t.Fatal("credentials in persisted failure")
			}
		})
	}
}

func TestNativeTorznabCapabilitiesHealthRequiresCredentialProof(t *testing.T) {
	testNativeTorznabCapabilitiesHealth(t, false)
}
func TestNativeTorznabManualTestRequiresCredentialProof(t *testing.T) {
	testNativeTorznabCapabilitiesHealth(t, true)
}
