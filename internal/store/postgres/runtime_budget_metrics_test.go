package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"testing"
	"time"
)

func TestRuntimeIndexerBudgetMetricsReadOnlyAndTransitionsPG(t *testing.T) {
	ctx := t.Context()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reader := postgres.NewRuntimeMetricsReader(pool)
	empty, err := reader.ReadRuntimeMetrics(ctx)
	if err != nil || empty.IndexerBudget != (ports.IndexerBudgetMetrics{}) {
		t.Fatal(empty, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO indexer(id,name,url,api_key_encrypted,health,next_query_at,rate_limit_until) VALUES
 (gen_random_uuid(),'open','https://private-endpoint',decode('aa','hex'),'online','-infinity',NULL),
 (gen_random_uuid(),'interval','https://private-endpoint',decode('aa','hex'),'online',statement_timestamp()+interval '1 hour',NULL),
 (gen_random_uuid(),'cooldown','https://private-endpoint',decode('aa','hex'),'rate_limited',statement_timestamp()+interval '1 hour',statement_timestamp()+interval '2 hours'),
 (gen_random_uuid(),'expired','https://private-endpoint',decode('aa','hex'),'rate_limited','-infinity',statement_timestamp()-interval '1 minute'),
 (gen_random_uuid(),'offline','https://private-endpoint',decode('aa','hex'),'offline','-infinity',NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if e := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(i) ORDER BY id)::text FROM indexer i`).Scan(&value); e != nil {
			t.Fatal(e)
		}
		return value
	}
	before := snapshot()
	current, err := reader.ReadRuntimeMetrics(ctx)
	b := current.IndexerBudget
	if err != nil || b.Open != 3 || b.Interval != 1 || b.Cooldown != 1 || b.MaxDelaySeconds < 7190 || b.MaxDelaySeconds > 7200 {
		t.Fatal(b, err)
	}
	if before != snapshot() {
		t.Fatal("scrape changed budget or health")
	}
	// Expiry clears only temporal gates, never changes health or authorizes native execution.
	if _, err = pool.Exec(ctx, `UPDATE indexer SET next_query_at=statement_timestamp()-interval '1 minute',rate_limit_until=statement_timestamp()-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	current, err = reader.ReadRuntimeMetrics(ctx)
	if err != nil || current.IndexerBudget != (ports.IndexerBudgetMetrics{Open: 5}) {
		t.Fatal(current.IndexerBudget, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "LOCK TABLE indexer IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	unavailable, err := reader.ReadRuntimeMetrics(ctx)
	if !errors.Is(err, ports.ErrUnavailable) || unavailable.IndexerBudget != (ports.IndexerBudgetMetrics{}) || len(unavailable.Queues) != 0 || time.Since(started) > 4*time.Second {
		t.Fatal("partial snapshot escaped failed budget collection", unavailable, err)
	}
}
