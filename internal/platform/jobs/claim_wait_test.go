package jobs_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
)

func TestJobClaimLeaseStartsAfterTableWait(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		priorClock bool
	}{
		{"cache_statement", "cache_statement", false},
		{"simple_protocol", "simple_protocol", false},
		{"prior_clock_negative_control", "simple_protocol", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedQueue, dsn := setupJobsTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			databaseURL, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			params := databaseURL.Query()
			params.Set("default_query_exec_mode", tc.mode)
			params.Set("client_encoding", "UTF8")
			databaseURL.RawQuery = params.Encode()
			pool, err := postgres.NewPool(ctx, databaseURL.String(), postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			queue := jobs.NewQueue(pool, idgen.New(), clock.New())
			if _, err := seedQueue.Enqueue(ctx, "claim_wait", "", 0, nil, time.Time{}, 3); err != nil {
				t.Fatal(err)
			}
			if tc.priorClock {
				// Reproduce the prior statement-clock behavior without changing production SQL.
				if _, err := pool.Exec(ctx, `CREATE FUNCTION claim_prior_clock_probe() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF OLD.state='queued' AND NEW.state='running' THEN
 NEW.lease_until:=statement_timestamp()+interval '500 milliseconds';
 END IF;
 RETURN NEW;
 END $$;
 CREATE TRIGGER claim_prior_clock_probe BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION claim_prior_clock_probe();`); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			var blocker int32
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, "LOCK TABLE job IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			const lease = 500 * time.Millisecond
			var claimed *db.Job
			var claimErr error
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				claimed, claimErr = queue.Claim(ctx, []string{"claim_wait"}, "wait-owner", lease)
			}()
			defer func() {
				cancel()
				_ = tx.Rollback(context.WithoutCancel(ctx))
				select {
				case <-stopped:
				case <-time.After(5 * time.Second):
					t.Error("claim did not stop")
				}
			}()
			var waitingSince time.Time
			// Record server time after observing an actual ungranted job-table lock.
			// Activity timestamps may be NULL and are not required for this lower bound.
			for {
				err := pool.QueryRow(ctx, "SELECT clock_timestamp() WHERE EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted AND locktype='relation' AND relation='job'::regclass AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND $1::int=ANY(pg_blocking_pids(pid)))", blocker).Scan(&waitingSince)
				if err == nil {
					break
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal(err)
				}
				select {
				case <-stopped:
					t.Fatalf("claim did not wait: %v", claimErr)
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			// This is a server-time lower bound taken while the table is still locked.
			var releasedAfter time.Time
			for {
				var now time.Time
				if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
					t.Fatal(err)
				}
				if now.After(waitingSince.Add(lease + 50*time.Millisecond)) {
					releasedAfter = now
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if claimErr != nil || claimed == nil {
				t.Fatalf("claim: %v", claimErr)
			}
			if !claimed.LeaseUntil.Valid {
				t.Fatal("claim omitted lease")
			}
			// Client scheduling and later round trips may consume the remaining lease.
			// Compare the persisted deadline with the pre-unlock server bound instead.
			earliestDeadline := releasedAfter.Add(lease)
			if tc.priorClock {
				if !claimed.LeaseUntil.Time.Before(releasedAfter) {
					t.Fatal("negative control did not reproduce expiration during the wait")
				}
				if !claimed.LeaseUntil.Time.Before(earliestDeadline) {
					t.Fatal("server bound failed to reject the prior clock")
				}
			} else if claimed.LeaseUntil.Time.Before(earliestDeadline) {
				t.Fatalf("claim lease began before table unlock: lease_until=%s lower_bound=%s", claimed.LeaseUntil.Time, earliestDeadline)
			}
		})
	}
}
