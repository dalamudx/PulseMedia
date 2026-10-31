package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestRuntimeMetricsReadOnlyAndUnavailablePG(t *testing.T) {
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
	sql := `INSERT INTO job(id,kind,state,payload,not_before,lease_owner,lease_token,lease_until) VALUES
 (gen_random_uuid(),'private-kind','queued','{"secret":"private-payload"}',statement_timestamp()-interval '2 minutes',NULL,0,NULL),
 (gen_random_uuid(),'private-kind','queued','{}',statement_timestamp()+interval '1 hour',NULL,0,NULL),
 (gen_random_uuid(),'private-kind','running','{}',statement_timestamp(),'owner',7,statement_timestamp()-interval '1 minute'),
 (gen_random_uuid(),'private-kind','dead','{}',statement_timestamp(),NULL,0,NULL),
 (gen_random_uuid(),'private-kind','succeeded','{}',statement_timestamp(),NULL,0,NULL);
 INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) VALUES ('00000000-0000-4000-8000-000000000001','private','private','private','{}',1);
 INSERT INTO outbox_delivery(id,event_id,consumer,aggregate_type,aggregate_id,seq,state,not_before,lease_owner,lease_token,lease_until) VALUES
 (gen_random_uuid(),'00000000-0000-4000-8000-000000000001','pending','private','private',1,'pending',statement_timestamp()-interval '1 minute',NULL,0,NULL),
 (gen_random_uuid(),'00000000-0000-4000-8000-000000000001','delivering','private','private',1,'delivering',statement_timestamp(),'owner',9,statement_timestamp()-interval '1 minute');`
	if _, err = pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	snapshot := func() []string {
		var values []string
		rows, err := pool.Query(ctx, `SELECT row_to_json(j)::text FROM job j UNION ALL SELECT row_to_json(d)::text FROM outbox_delivery d ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			values = append(values, v)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return values
	}
	before := snapshot()
	reader := postgres.NewRuntimeMetricsReader(pool)
	result, err := reader.ReadRuntimeMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]ports.QueueMetrics{}
	for _, row := range result.Queues {
		found[row.Queue+"/"+row.State] = row
	}
	if row := found["job/queued"]; row.Count != 2 || row.ReadyAgeSeconds < 119 || row.ReadyAgeSeconds > 150 {
		t.Fatal(row)
	}
	if found["job/running"].ExpiredLeases != 1 || found["job/dead"].Count != 1 || found["outbox/pending"].Count != 1 || found["outbox/delivering"].ExpiredLeases != 1 {
		t.Fatal(found)
	}
	if _, ok := found["job/succeeded"]; ok {
		t.Fatal("successful history counted as backlog")
	}
	if result.PoolMax <= 0 || result.AcquireCount <= 0 || !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("invalid pool observation or metrics mutated rows")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = reader.ReadRuntimeMetrics(cancelled); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "LOCK TABLE job IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	unavailable, err := reader.ReadRuntimeMetrics(ctx)
	if !errors.Is(err, ports.ErrUnavailable) || len(unavailable.Queues) != 0 || time.Since(started) > 4*time.Second {
		t.Fatal("blocked collection not bounded", err, time.Since(started))
	}
}
