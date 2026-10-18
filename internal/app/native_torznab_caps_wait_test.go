package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativeTorznabSecondProbeRechecksAuthorityAfterLock(t *testing.T) {
	for _, change := range []string{"valid", "config", "expired", "token", "owner", "state", "scope", "cooling", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var searches atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("t") == "caps" {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					_, _ = fmt.Fprint(w, `<caps><searching><movie-search available="yes" supportedParams="q"/></searching></caps>`)
					return
				}
				searches.Add(1)
				_, _ = fmt.Fprint(w, `<rss><channel/></rss>`)
			}))
			defer server.Close()
			defer unblock()
			key := bytes.Repeat([]byte{7}, 32)
			cipher, e := crypto.Encrypt(key, []byte("synthetic-key"))
			if e != nil {
				t.Fatal(e)
			}
			q := postgres.Queries(f.pool)
			idx, e := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: f.gen.NewUUID(), Name: change, Url: server.URL, ApiKeyEncrypted: cipher, Categories: []byte("[]")})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE indexer SET rss_enabled=true,health='offline',health_next_check_at=clock_timestamp() WHERE id=$1", idx.ID); e != nil {
				t.Fatal(e)
			}
			cfg := f.pool.Config()
			cfg.MaxConns, cfg.MinConns = 3, 0
			cfg.ConnConfig.RuntimeParams["application_name"] = "movie-link-waiter"
			consumerPool, e := pgxpool.NewWithConfig(ctx, cfg)
			if e != nil {
				t.Fatal(e)
			}
			defer consumerPool.Close()
			svc := search.NewService(postgres.NewSearchExecutionStore(consumerPool), f.gen, crypto.NewSecretProtector(key), 0)
			svc.RegisterClient(idx.ID, torznab.NewClient(server.URL, "synthetic-key", server.Client()))
			repo := postgres.NewIndexerHealthStore(consumerPool, f.gen)
			health := search.NewHealthService(repo, svc)
			if n, e := health.QueueDue(ctx, 1); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			queue := jobs.NewQueue(f.pool, f.gen, clock.New())
			j, e := queue.Claim(ctx, []string{"indexer_health"}, "caps-owner", 30*time.Second)
			if e != nil || j == nil {
				t.Fatal(j, e)
			}
			var work ports.IndexerHealthJob
			if e = json.Unmarshal(j.Payload, &work); e != nil {
				t.Fatal(e)
			}
			work.ID, work.Owner, work.Token = j.ID, "caps-owner", j.LeaseToken
			workerCtx, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan error, 1)
			go func() { done <- health.ExecuteJob(workerCtx, work) }()
			joined := false
			defer func() {
				stop()
				unblock()
				if !joined {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("worker did not drain")
					}
				}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("caps not reached")
			}
			tx, e := f.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			var before time.Time
			if e = tx.QueryRow(ctx, "SELECT next_query_at FROM indexer WHERE id=$1 FOR UPDATE", idx.ID).Scan(&before); e != nil {
				t.Fatal(e)
			}
			unblock()
			waitMovieLinkLock(t, f)
			var mutation string
			switch change {
			case "config":
				_, e = tx.Exec(ctx, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", idx.ID)
			case "cooling":
				_, e = tx.Exec(ctx, "UPDATE indexer SET rate_limit_until=clock_timestamp()+interval '1 minute' WHERE id=$1", idx.ID)
			case "expired":
				mutation = "lease_until=clock_timestamp()-interval '1 second'"
			case "token":
				mutation = "lease_token=lease_token+1"
			case "owner":
				mutation = "lease_owner='replacement-owner'"
			case "state":
				mutation = "state='dead'"
			case "scope":
				mutation = "payload=jsonb_set(payload,'{config_version}','99999')"
			case "cancel":
				stop()
			}
			if e != nil {
				t.Fatal(e)
			}
			if mutation != "" {
				if _, e = f.pool.Exec(ctx, "UPDATE job SET "+mutation+" WHERE id=$1", j.ID); e != nil {
					t.Fatal(e)
				}
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			select {
			case e = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal("health did not finish")
			}
			if change == "valid" && e != nil {
				t.Fatal(e)
			}
			var after time.Time
			var healthState string
			var candidates, audits, events int
			if e = f.pool.QueryRow(ctx, `SELECT next_query_at,health,(SELECT count(*) FROM release_candidate WHERE indexer_id=$1),(SELECT count(*) FROM audit_log WHERE action='indexer.health_observed' AND target_id=$1::text),(SELECT count(*) FROM outbox_event WHERE event_type='indexer.health_observed' AND aggregate_id=$1::text) FROM indexer WHERE id=$1`, idx.ID).Scan(&after, &healthState, &candidates, &audits, &events); e != nil {
				t.Fatal(e)
			}
			if candidates != 0 {
				t.Fatal("health wrote candidates")
			}
			if change == "valid" {
				if searches.Load() != 1 || healthState != "online" || !after.After(before) || audits != 1 || events != 1 {
					t.Fatal("valid probe failed", searches.Load(), healthState, audits, events)
				}
			} else {
				if searches.Load() != 0 || healthState == "online" || !after.Equal(before) {
					t.Fatal("stale second probe changed budget or sent request", searches.Load(), healthState, before, after)
				}
				if change != "cooling" && (audits != 0 || events != 0) {
					t.Fatal("unauthorized health observation", audits, events)
				}
			}
		})
	}
}
