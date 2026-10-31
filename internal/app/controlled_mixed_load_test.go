package app

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
)

// Explicitly gated: the full release check runs 30m separately, while a short
// smoke can validate the harness. A short smoke is not a soak acceptance receipt.
func TestAppControlledMixedLoadPG(t *testing.T) {
	raw := os.Getenv("PULSE_MIXED_LOAD_DURATION")
	if raw == "" {
		t.Skip("requires explicit PULSE_MIXED_LOAD_DURATION; run 30m for acceptance")
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < 15*time.Second || duration > time.Hour {
		t.Fatal("duration must be 15s..1h")
	}
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err = postgres.Migrate(t.Context(), dsn, "../../migrations"); err != nil {
		t.Fatal("migration failed", err)
	}
	control, err := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal("control pool unavailable")
	}
	defer control.Close()
	gen := idgen.New()
	f := &organizationFixture{pool: control, q: db.New(control), gen: gen}
	var mu sync.Mutex
	attempts := map[string]int{}
	delivered := map[string]bool{}
	duplicates := 0
	rateLimited := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-PulseMedia-Event-ID")
		mu.Lock()
		defer mu.Unlock()
		if id == "" {
			w.WriteHeader(400)
			return
		}
		attempts[id]++
		if attempts[id] == 1 && len(attempts)%31 == 0 {
			rateLimited++
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		if delivered[id] {
			duplicates++
		}
		delivered[id] = true
		w.WriteHeader(200)
	}))
	defer server.Close()
	channel := createOutboxChannel(t, f, nil, server.URL)
	actor := notificationFixtureAdmin(t, f)
	cipher, err := crypto.NewSecretProtector(outboxTestKey).EncryptSecret([]byte(`{"server_url":"https://cloud.example","user_key":"owned","password":"owned"}`))
	if err != nil {
		t.Fatal(err)
	}
	cipher[len(cipher)-1] ^= 1
	if _, err = control.Exec(t.Context(), `INSERT INTO cookiecloud_configuration(owner_id,version,enabled,interval_seconds,server_origin,connection_encrypted,created_at,updated_at) VALUES($1,1,true,60,'https://cloud.example',$2,'2000-01-01','2000-01-01')`, actor.ID, cipher); err != nil {
		t.Fatal("seed failed", err)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = dsn
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	var published, samples int64
	var maxRead time.Duration
	var maxConnections int32
	var firstHeap uint64
	firstGoroutines := 0
	// One consumer handles at most one attempt per second. Leave capacity for
	// Retry-After retries so this checks bounded steady load, not intentional overload.
	publishEvery := 2 * time.Second
	started := time.Now()
	for phase := 0; phase < 3; phase++ {
		func() {
			a, err := New(cfg, WithLogger(logging.NewLogger(io.Discard, "error")))
			if err != nil {
				t.Fatal("App reconstruction failed", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Error("App failed", err)
					}
				case <-time.After(30 * time.Second):
					t.Error("App shutdown exceeded budget")
				}
				_ = a.Close()
			}()
			ready, stop := context.WithTimeout(ctx, 15*time.Second)
			waitOutboxCondition(t, ready, func() bool { return a.Readyz(ready).Ready })
			stop()
			pool := a.dbPool
			ob := outbox.New(pool, gen, clock.New())
			retention := postgres.NewHistoryRetentionStore(pool)
			until := time.Now().Add(duration / 3)
			ticker := time.NewTicker(publishEvery)
			defer ticker.Stop()
			for time.Now().Before(until) {
				func() {
					call, release := context.WithTimeout(ctx, 15*time.Second)
					defer release()
					// Keep three long-held sessions; the fourth physical slot belongs to
					// real delivery workers. The eight-connection pool must still serve reads.
					for range 3 {
						held, err := postgres.AcquirePhysicalConnection(call, pool)
						if err != nil {
							t.Fatalf("physical admission failed: deadline=%t canceled=%t acquired=%d", errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), pool.Stat().AcquiredConns())
						}
						defer held.Release()
					}
					tx, err := pool.Begin(call)
					if err != nil {
						t.Fatal("short transaction starved")
					}
					defer func() { _ = tx.Rollback(context.Background()) }()
					if _, err = ob.Publish(call, db.New(tx), "library", uuid.NewString(), "media.imported", map[string]string{"title": "owned mixed load"}, []string{"notify:" + channel.String()}); err != nil {
						t.Fatal("publish failed", err)
					}
					if err = tx.Commit(call); err != nil {
						t.Fatal("commit failed", err)
					}
					published++
					begin := time.Now()
					metrics, err := a.RuntimeMetricsReader().ReadRuntimeMetrics(call)
					if err != nil {
						t.Fatal("metrics starved", err)
					}
					maxRead = max(maxRead, time.Since(begin))
					maxConnections = max(maxConnections, metrics.PoolTotal)
					samples++
					if metrics.PoolTotal > metrics.PoolMax || metrics.PoolMax != 8 {
						t.Fatal("pool limit changed", metrics.PoolTotal, metrics.PoolMax)
					}
					if _, err = retention.PruneHistory(call); err != nil {
						t.Fatal("retention stalled", err)
					}
					var pending int
					if err = pool.QueryRow(call, `SELECT count(*) FROM outbox_delivery WHERE state<>'delivered'`).Scan(&pending); err != nil || pending > 20 {
						t.Fatal("delivery backlog not bounded", pending, err)
					}
				}()
				select {
				case <-ctx.Done():
					t.Fatal("runtime stopped early")
				case <-ticker.C:
				}
			}
		}()
		runtime.GC()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		goroutines := runtime.NumGoroutine()
		if phase == 0 {
			firstHeap = mem.HeapAlloc
			firstGoroutines = goroutines
		} else if mem.HeapAlloc > firstHeap+64*1024*1024 || goroutines > firstGoroutines+20 {
			t.Fatal("resources did not settle after reconstruction", mem.HeapAlloc, goroutines)
		}
		mu.Lock()
		sent, limited := len(delivered), rateLimited
		mu.Unlock()
		t.Logf("MIXED_PHASE phase=%d published=%d delivered=%d retry_after_responses=%d samples=%d heap_bytes=%d goroutines=%d", phase+1, published, sent, limited, samples, mem.HeapAlloc, goroutines)
	}
	// Drain persisted deliveries using another real App after the last reconstruction.
	a, err := New(cfg, WithLogger(logging.NewLogger(io.Discard, "error")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		err := <-done
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		_ = a.Close()
	}()
	// Even an empty queue must wait for startup before the final cancellation.
	waitOutboxCondition(t, ctx, func() bool { return a.Readyz(ctx).Ready })
	waitOutboxCondition(t, ctx, func() bool {
		var pending int
		err := control.QueryRow(ctx, `SELECT count(*) FROM outbox_delivery WHERE state<>'delivered'`).Scan(&pending)
		return err == nil && pending == 0
	})
	mu.Lock()
	sent := len(delivered)
	repeated := duplicates
	mu.Unlock()
	if int64(sent) != published || repeated != 0 {
		t.Fatal("delivery identity lost across reconstruction", published, sent, repeated)
	}
	t.Logf("MIXED_LOAD_PASS elapsed_seconds=%.1f published=%d delivered=%d metric_reads=%d max_metric_read_ms=%.1f max_pool=%d restarts=3 duplicate_success=0 external_services=NONE", time.Since(started).Seconds(), published, sent, samples, float64(maxRead.Microseconds())/1000, maxConnections)
}
