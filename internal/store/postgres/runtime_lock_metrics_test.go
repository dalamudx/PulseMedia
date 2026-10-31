package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeLockMetricsDMLIsolationAndReleasePG(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cluster := pgtest.GetCluster(t)
	dsn := cluster.NewTestDB(t)
	otherDSN := cluster.NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	role := "metrics_" + uuid.NewString()[:8]
	quoted := pgx.Identifier{role}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE ROLE "+quoted+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, e := admin.Exec(context.Background(), "DROP OWNED BY "+quoted+"; DROP ROLE "+quoted); e != nil {
			t.Error(e)
		}
	})
	if _, err = admin.Exec(ctx, "GRANT USAGE ON SCHEMA public TO "+quoted+"; GRANT SELECT ON job,outbox_delivery,indexer TO "+quoted); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User = role
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var elevated bool
	if err = pool.QueryRow(ctx, `SELECT rolsuper OR pg_has_role(current_user,'pg_read_all_stats','MEMBER') FROM pg_roles WHERE rolname=current_user`).Scan(&elevated); err != nil || elevated {
		t.Fatal("metrics must not need elevated monitoring access", elevated, err)
	}
	reader := postgres.NewRuntimeMetricsReader(pool)
	initial, err := reader.ReadRuntimeMetrics(ctx)
	if err != nil || initial.LockWaiters != 0 || initial.LockWaitAgeSeconds != 0 {
		t.Fatal(initial, err)
	}

	// Each waiter owns its connection and has a cancel/join cleanup, including failed assertions.
	block := func(url, user string, key int64) (func(), func()) {
		t.Helper()
		holder, e := pgx.Connect(ctx, url)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = holder.Close(context.Background()) })
		if _, e = holder.Exec(ctx, "SELECT pg_advisory_lock($1)", key); e != nil {
			t.Fatal(e)
		}
		config, e := pgx.ParseConfig(url)
		if e != nil {
			t.Fatal(e)
		}
		if user != "" {
			config.User = user
		}
		waiter, e := pgx.ConnectConfig(ctx, config)
		if e != nil {
			t.Fatal(e)
		}
		waitCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		var waitErr error
		waiterPID := waiter.PgConn().PID()
		go func() {
			defer close(done)
			_, waitErr = waiter.Exec(waitCtx, "SELECT pg_advisory_lock($1)", key)
			_ = waiter.Close(context.Background())
		}()
		joined := false
		join := func() {
			if !joined {
				stop()
				<-done
				joined = true
			}
		}
		t.Cleanup(join)
		release := func() {
			t.Helper()
			if _, e := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", key); e != nil {
				t.Fatal(e)
			}
			<-done
			if waitErr != nil {
				t.Fatal("waiter did not finish after lock release", waitErr)
			}
			join()
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			var waiting bool
			if e = admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted AND waitstart IS NOT NULL)`, waiterPID).Scan(&waiting); e != nil {
				t.Fatal(e)
			}
			if waiting {
				return join, release
			}
			select {
			case <-tick.C:
			case <-done:
				t.Fatal("waiter exited before blocking")
			case <-deadline.C:
				t.Fatal("waiter did not block")
			}
		}
	}
	own, releaseOwn := block(dsn, role, 91009)
	foreignRole, _ := block(dsn, "", 91010)
	foreignDB, _ := block(otherDSN, role, 91011)
	// Join before pool/role cleanup; t.Cleanup is also registered for early fatal paths.
	defer foreignDB()
	defer foreignRole()
	defer own()
	observed, err := reader.ReadRuntimeMetrics(ctx)
	if err != nil || observed.LockWaiters != 1 || observed.LockWaitAgeSeconds <= 0 || observed.LockWaitAgeSeconds > 10 {
		t.Fatal(fmt.Sprintf("want one own-role/own-db waiter, got %+v", observed), err)
	}
	releaseOwn()
	released, err := reader.ReadRuntimeMetrics(ctx)
	if err != nil || released.LockWaiters != 0 || released.LockWaitAgeSeconds != 0 {
		t.Fatal("released wait remains, or foreign wait leaked", released, err)
	}
}
