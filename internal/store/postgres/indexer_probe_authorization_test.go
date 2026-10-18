package postgres_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestIndexerProbeCurrentAccountAfterLockPG(t *testing.T) {
	for _, change := range []string{"role='member'", "is_active=false"} {
		t.Run(change, func(t *testing.T) {
			p, _, secrets, actor, store := managementFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			var requests atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, `<caps><searching><movie-search available="yes" supportedParams="q"/></searching></caps>`)
			}))
			defer source.Close()
			mutation := managementCreate(t, secrets)
			var err error
			mutation.Configuration.DisplayURL = source.URL
			mutation.Configuration.URLCiphertext, err = secrets.EncryptSecret([]byte(source.URL + "/api"))
			if err != nil {
				t.Fatal("prepare source failed")
			}
			receipt, err := store.MutateIndexer(ctx, actor, mutation, "probe-account-lock", managementHash)
			if err != nil {
				t.Fatal("create source failed", err)
			}
			codec := crypto.NewSecretProtector(secrets.key)
			svc := search.NewService(postgres.NewSearchExecutionStore(p, codec), managementIDs{}, codec)
			svc.SetConfiguredClientFactory(func(_ ports.IndexerProtocol, endpoint, key string) ports.IndexerClient {
				return torznab.NewClient(endpoint, key, source.Client())
			})
			snapshot := func() string {
				t.Helper()
				var s string
				if err := p.QueryRow(ctx, `SELECT jsonb_build_array(next_query_at,health,rate_limit_until,health_observed_at)::text FROM indexer WHERE id=$1`, receipt.Indexer.ID).Scan(&s); err != nil {
					t.Fatal("read probe state failed")
				}
				return s
			}
			before := snapshot()
			tx, err := p.Begin(ctx)
			if err != nil {
				t.Fatal("begin account change failed")
			}
			if _, err = tx.Exec(ctx, "UPDATE app_user SET "+change+" WHERE id=$1", actor.ID); err != nil {
				_ = tx.Rollback(ctx)
				t.Fatal("hold account failed")
			}
			done := make(chan error, 1)
			joined := false
			go func() { _, _, _, e := svc.TestIndexerForActor(ctx, actor, receipt.Indexer.ID); done <- e }()
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = tx.Rollback(cleanup)
				if !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("probe did not join")
					}
				}
			}()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var blocked bool
				err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid)))`, tx.Conn().PgConn().PID()).Scan(&blocked)
				if err != nil {
					t.Fatal("read account lock wait failed")
				}
				if blocked {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("probe did not wait on current account")
				case <-ticker.C:
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal("commit account change failed")
			}
			select {
			case err = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal("probe did not finish after account change")
			}
			if !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("probe accepted changed account", err)
			}
			if requests.Load() != 0 || snapshot() != before {
				t.Fatal("unauthorized probe executed or changed health/budget")
			}
			var queries int
			if err = p.QueryRow(ctx, `SELECT count(*) FROM indexer_query WHERE indexer_id=$1`, receipt.Indexer.ID).Scan(&queries); err != nil || queries != 0 {
				t.Fatal("unauthorized probe persisted query")
			}
			release, err := postgres.AcquireIndexerPermit(ctx, p, receipt.Indexer.ID)
			if err != nil {
				t.Fatal("probe failed to release physical permit")
			}
			release()
		})
	}
}
