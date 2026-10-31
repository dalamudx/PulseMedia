package outbox_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func durationFixture(t *testing.T) (*pgxpool.Pool, *outbox.Outbox) {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, migrationsDir); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, outbox.New(pool, idgen.New(), clock.New())
}
func seedDurationDelivery(t *testing.T, pool *pgxpool.Pool, ob *outbox.Outbox, consumer string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	event, err := ob.Publish(ctx, db.New(tx), "duration", uuid.NewString(), "duration.test", nil, []string{consumer})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err = pool.QueryRow(ctx, "SELECT id FROM outbox_delivery WHERE event_id=$1", event.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestOutboxInvalidDurationsPreserveState(t *testing.T) {
	pool, ob := durationFixture(t)
	ctx := context.Background()
	for _, action := range []string{"claim", "fail"} {
		for _, duration := range []time.Duration{time.Nanosecond, (time.Duration(math.MaxInt32) + 1) * time.Millisecond, ((1 << 32) + 1000) * time.Millisecond} {
			t.Run(action+"/"+duration.String(), func(t *testing.T) {
				consumer := uuid.NewString()
				id := seedDurationDelivery(t, pool, ob, consumer)
				var d *db.OutboxDelivery
				var err error
				if action == "fail" {
					d, err = ob.Claim(ctx, consumer, "owner", time.Minute)
					if err != nil || d == nil {
						t.Fatalf("claim: %v", err)
					}
				}
				snapshot := func() string {
					var raw string
					if e := pool.QueryRow(ctx, "SELECT to_jsonb(d)::text FROM outbox_delivery d WHERE id=$1", id).Scan(&raw); e != nil {
						t.Fatal(e)
					}
					return raw
				}
				before := snapshot()
				if action == "claim" {
					_, err = ob.Claim(ctx, consumer, "owner", duration)
				} else {
					err = ob.Fail(ctx, id, "owner", d.LeaseToken, "synthetic", duration)
				}
				if !errors.Is(err, outbox.ErrInvalidDuration) {
					t.Errorf("invalid duration classification: %v", err)
				}
				if snapshot() != before {
					t.Error("invalid duration mutated delivery")
				}
			})
		}
	}
}
func TestOutboxDurationDefaultsAndMaximum(t *testing.T) {
	pool, ob := durationFixture(t)
	ctx := context.Background()
	for _, value := range []time.Duration{0, -time.Millisecond, time.Duration(math.MaxInt32) * time.Millisecond} {
		t.Run(value.String(), func(t *testing.T) {
			consumer := uuid.NewString()
			id := seedDurationDelivery(t, pool, ob, consumer)
			d, err := ob.Claim(ctx, consumer, "owner", value)
			if err != nil || d == nil {
				t.Fatalf("claim: %v", err)
			}
			assertRemaining := func(column string, want time.Duration) {
				t.Helper()
				var remaining float64
				if e := pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM "+column+"-clock_timestamp())::float8 FROM outbox_delivery WHERE id=$1", id).Scan(&remaining); e != nil {
					t.Fatal(e)
				}
				if remaining > want.Seconds() || remaining < want.Seconds()*0.9 {
					t.Fatalf("%s remaining=%v want=%v", column, remaining, want)
				}
			}
			claim, retry := value, value
			if value <= 0 {
				claim = 30 * time.Second
				retry = 5 * time.Second
			}
			assertRemaining("lease_until", claim)
			if err = ob.Fail(ctx, id, "owner", d.LeaseToken, "synthetic", value); err != nil {
				t.Fatal(err)
			}
			assertRemaining("not_before", retry)
		})
	}
}
func TestOutboxClaimLeaseStartsAfterTableWait(t *testing.T) {
	seedPool, seedOutbox := durationFixture(t)
	for _, mode := range []pgx.QueryExecMode{pgx.QueryExecModeCacheStatement, pgx.QueryExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cfg := seedPool.Config()
			cfg.ConnConfig.DefaultQueryExecMode = mode
			cfg.ConnConfig.RuntimeParams["client_encoding"] = "UTF8"
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			ob := outbox.New(pool, idgen.New(), clock.New())
			consumer := uuid.NewString()
			// Prepare the normal query before taking the table lock.
			if _, err = ob.Claim(ctx, consumer, "owner", time.Second); err != nil {
				t.Fatal(err)
			}
			seedDurationDelivery(t, seedPool, seedOutbox, consumer)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			var blocker int32
			if err = tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, "LOCK TABLE outbox_delivery IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			const lease = 500 * time.Millisecond
			var claimed *db.OutboxDelivery
			var claimErr error
			done := make(chan struct{})
			go func() { defer close(done); claimed, claimErr = ob.Claim(ctx, consumer, "owner", lease) }()
			defer func() { cancel(); _ = tx.Rollback(context.WithoutCancel(ctx)); <-done }()
			var waiting time.Time
			for {
				err = pool.QueryRow(ctx, "SELECT clock_timestamp() WHERE EXISTS(SELECT 1 FROM pg_locks WHERE NOT granted AND relation='outbox_delivery'::regclass AND $1::int=ANY(pg_blocking_pids(pid)))", blocker).Scan(&waiting)
				if err == nil {
					break
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal(err)
				}
				select {
				case <-done:
					t.Fatalf("claim did not wait: %v", claimErr)
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			var releasedAfter time.Time
			for {
				if err = pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&releasedAfter); err != nil {
					t.Fatal(err)
				}
				if releasedAfter.After(waiting.Add(lease + 50*time.Millisecond)) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if claimErr != nil || claimed == nil {
				t.Fatalf("claim: %v", claimErr)
			}
			if !claimed.LeaseUntil.Valid || claimed.LeaseUntil.Time.Before(releasedAfter.Add(lease)) {
				t.Fatal("lock wait consumed the claimed lease")
			}
		})
	}
}
