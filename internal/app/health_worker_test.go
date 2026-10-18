package app

import (
	"context"
	"encoding/hex"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppRunRecoversHealthWhileAnotherEndpointIsSlow(t *testing.T) {
	f := organizationSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var healthy atomic.Bool
	var observations atomic.Int32
	slowStarted := make(chan struct{}, 1)
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observations.Add(1)
		if !healthy.Load() {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, "passkey=fixture-private-do-not-persist")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			_, _ = io.WriteString(w, `<caps><searching><movie-search available="yes" supportedParams="q,imdbid"/></searching></caps>`)
			return
		}
		if r.URL.Query().Get("t") != "movie" || r.URL.Query().Get("limit") != "1" {
			t.Error("unexpected credential proof request")
		}
		_, _ = io.WriteString(w, "<rss><channel></channel></rss>")
	}))
	defer fast.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slowStarted <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer slow.Close()
	cipher, e := crypto.Encrypt(outboxTestKey, []byte("health-fixture-secret"))
	if e != nil {
		t.Fatal(e)
	}
	fastID, slowID := uuid.New(), uuid.New()
	for _, entry := range []struct {
		id  uuid.UUID
		url string
	}{{fastID, fast.URL}, {slowID, slow.URL}} {
		if _, e = f.q.CreateIndexer(ctx, db.CreateIndexerParams{ID: entry.id, Name: "health-runtime", Url: entry.url, ApiKeyEncrypted: cipher, Categories: []byte(`[]`)}); e != nil {
			t.Fatal(e)
		}
		if _, e = f.pool.Exec(ctx, "UPDATE indexer SET rss_enabled=true,rss_next_poll_at=clock_timestamp()+interval '1 year',health='offline',health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id=$1", entry.id); e != nil {
			t.Fatal(e)
		}
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, e := New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if e != nil {
		t.Fatal(e)
	}
	runtime, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runtime) }()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("health workers/physical I/O did not cancel and join")
		}
	}()
	select {
	case <-slowStarted:
	case <-ctx.Done():
		t.Fatal("slow endpoint never started")
	}
	waitOutboxCondition(t, ctx, func() bool {
		r, e := f.q.GetIndexerByID(ctx, fastID)
		return e == nil && r.HealthReason == "unavailable" && r.HealthFailures == 1 && r.HealthCheckedAt.Valid
	})
	healthy.Store(true)
	if _, e = f.pool.Exec(ctx, "UPDATE indexer SET health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id=$1", fastID); e != nil {
		t.Fatal(e)
	}
	waitOutboxCondition(t, ctx, func() bool {
		r, e := f.q.GetIndexerByID(ctx, fastID)
		return e == nil && r.Health == "online" && r.HealthReason == "ok" && r.HealthFailures == 0
	})
	if observations.Load() != 3 {
		t.Fatal("probe recovery unexpectedly repeated physical query", observations.Load())
	}
	var jobCount int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='indexer_health' AND state='succeeded' AND payload->>'indexer_id'=$1", fastID.String()).Scan(&jobCount); e != nil || jobCount != 2 {
		t.Fatal("health receipt not durable", jobCount, e)
	}
	var leak bool
	if e = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM indexer_query WHERE error LIKE '%fixture-private%') OR EXISTS(SELECT 1 FROM audit_log WHERE payload::text LIKE '%fixture-private%') OR EXISTS(SELECT 1 FROM outbox_event WHERE payload::text LIKE '%fixture-private%')").Scan(&leak); e != nil || leak {
		t.Fatal("remote health body leaked", leak, e)
	}
}
