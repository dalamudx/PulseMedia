package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type personExecutionFixture struct {
	personScheduleFixture
	history124 string
	id         uuid.UUID
	lease      ports.JobLease
}

func newPersonExecutionFixture(t *testing.T) personExecutionFixture {
	t.Helper()
	f := newPersonScheduleFixture(t)
	history := repositoryMigrationsThrough(t, 124)
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), history); err != nil || version != 124 {
		t.Fatal(version, err)
	}
	id := uuid.New()
	f.save(t, id, f.input(), "original")
	f.schedule(t)
	var job uuid.UUID
	if err := f.p.QueryRow(t.Context(), `UPDATE job SET state='running',lease_owner='person-worker',lease_token=1,lease_until=clock_timestamp()+interval '5 minutes',attempts=1 WHERE kind='person_follow_sync' RETURNING id`).Scan(&job); err != nil {
		t.Fatal(err)
	}
	return personExecutionFixture{f, history, id, ports.JobLease{ID: job, Owner: "person-worker", Token: 1}}
}
func (f personExecutionFixture) begin(t *testing.T) *postgres.PersonFollowExecution {
	t.Helper()
	execution, err := f.scheduler.BeginExecution(t.Context(), f.lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(execution.Close)
	return execution
}
func (f personExecutionFixture) executionCounts(t *testing.T) []int {
	t.Helper()
	var total, released int
	if err := f.p.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE released_at IS NOT NULL) FROM person_follow_execution`).Scan(&total, &released); err != nil {
		t.Fatal(err)
	}
	return []int{total, released}
}
func (f personExecutionFixture) nextLease(t *testing.T) ports.JobLease {
	t.Helper()
	lease := f.lease
	lease.Token++
	f.exec(t, `UPDATE job SET lease_token=$2,lease_until=clock_timestamp()+interval '5 minutes',attempts=2 WHERE id=$1`, lease.ID, lease.Token)
	return lease
}
func TestPersonFollowExecutionExclusiveReleasePG(t *testing.T) {
	f := newPersonExecutionFixture(t)
	before := f.counts(t)
	execution := f.begin(t)
	work := execution.Snapshot()
	if work.ExecutionID == uuid.Nil || work.Config.ID != f.id || work.Config.Version != 1 || work.Actor.ID != f.actor.ID || work.RunID == uuid.Nil || work.Start != 0 || work.SourceStored {
		t.Fatal("execution lost frozen identity")
	}
	*work.Config.Settings.MovieRootID = uuid.New()
	work.Config.Settings.PersonID++
	if err := execution.Check(t.Context()); err != nil {
		t.Fatal("caller altered private execution", err)
	}
	if next, err := f.scheduler.BeginExecution(t.Context(), f.lease); !errors.Is(err, ports.ErrPersonFollowConflict) || next != nil {
		if next != nil {
			next.Close()
		}
		t.Fatal("parallel execution authorized", err)
	}
	if ready, err := f.scheduler.ExecutionReleased(t.Context(), f.lease.ID); err != nil || ready {
		t.Fatal("physical holder reported released", err)
	}
	if _, err := f.p.Exec(t.Context(), `UPDATE person_follow_execution SET released_at=clock_timestamp()`); err == nil {
		t.Fatal("another session forged release evidence")
	}
	execution.Close()
	execution.Close()
	if got := f.executionCounts(t); !reflect.DeepEqual(got, []int{1, 1}) {
		t.Fatal("release not recorded once", got)
	}
	if ready, err := f.scheduler.ExecutionReleased(t.Context(), f.lease.ID); err != nil || !ready {
		t.Fatal("completed physical call remained blocked", err)
	}
	if err := execution.Check(t.Context()); !errors.Is(err, ports.ErrPersonFollowConflict) {
		t.Fatal("closed execution accepted writes", err)
	}
	lease := f.nextLease(t)
	next, err := f.scheduler.BeginExecution(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	if !reflect.DeepEqual(f.executionCounts(t), []int{2, 2}) || !reflect.DeepEqual(before, f.counts(t)) {
		t.Fatal("reclaim modified protected ledgers")
	}
}
func TestPersonFollowExecutionLeaseExpiryRetainsPhysicalPG(t *testing.T) {
	f := newPersonExecutionFixture(t)
	execution := f.begin(t)
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.lease.ID)
	if err := execution.Check(t.Context()); !errors.Is(err, ports.ErrPersonFollowConflict) {
		t.Fatal("expired execution accepted authority", err)
	}
	lease := f.nextLease(t)
	if next, err := f.scheduler.BeginExecution(t.Context(), lease); !errors.Is(err, ports.ErrPersonFollowConflict) || next != nil {
		if next != nil {
			next.Close()
		}
		t.Fatal("lease change released physical exclusivity", err)
	}
	if ready, err := f.scheduler.ExecutionReleased(t.Context(), f.lease.ID); err != nil || ready {
		t.Fatal("lease expiry pretended physical return", err)
	}
	execution.Close()
	next, err := f.scheduler.BeginExecution(t.Context(), lease)
	if err != nil {
		t.Fatal("physical release did not allow new lease", err)
	}
	next.Close()
	if !reflect.DeepEqual(f.executionCounts(t), []int{2, 2}) {
		t.Fatal("lease-specific evidence lost")
	}
}
func TestPersonFollowExecutionSessionLossBlocksReplayPG(t *testing.T) {
	f := newPersonExecutionFixture(t)
	execution := f.begin(t)
	var pid int32
	if err := f.p.QueryRow(t.Context(), `SELECT backend_pid FROM person_follow_execution`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var killed bool
	if err := f.p.QueryRow(t.Context(), `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); err != nil || !killed {
		t.Fatal("owned physical session termination failed", err)
	}
	if err := execution.Check(t.Context()); err == nil {
		t.Fatal("lost session accepted authority")
	}
	if ready, err := f.scheduler.ExecutionReleased(t.Context(), f.lease.ID); err != nil || ready {
		t.Fatal("lock disappearance treated as return evidence", err)
	}
	lease := f.nextLease(t)
	if next, err := f.scheduler.BeginExecution(t.Context(), lease); !errors.Is(err, ports.ErrPersonFollowConflict) || next != nil {
		if next != nil {
			next.Close()
		}
		t.Fatal("unknown physical result replayed", err)
	}
	execution.Close()
	if !reflect.DeepEqual(f.executionCounts(t), []int{1, 0}) {
		t.Fatal("lost session forged a release")
	}
}
func TestPersonFollowExecutionCurrentAuthorityPG(t *testing.T) {
	for index, mutation := range []string{"UPDATE app_user SET is_active=false", "UPDATE person_follow_config SET enabled=false,version=version+1,updated_at=clock_timestamp()", "UPDATE library_root SET health='offline'", "UPDATE policy SET current_revision=current_revision+1", "UPDATE job SET payload='{}'::jsonb", "UPDATE job SET lease_owner='foreign-worker'"} {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			f := newPersonExecutionFixture(t)
			before := f.counts(t)
			f.exec(t, mutation)
			if execution, err := f.scheduler.BeginExecution(t.Context(), f.lease); err == nil || execution != nil {
				if execution != nil {
					execution.Close()
				}
				t.Fatal("stale authority acquired execution")
			}
			if !reflect.DeepEqual(f.executionCounts(t), []int{0, 0}) {
				t.Fatal("rejected authority wrote an execution")
			}
			if !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("rejected execution changed business ledgers")
			}
		})
	}
}
func TestPersonFollowExecutionCommitRollbackPG(t *testing.T) {
	for index, mutation := range []string{"RAISE EXCEPTION 'owned execution tail'", "UPDATE app_user SET is_active=false", "UPDATE person_follow_config SET enabled=false,version=version+1,updated_at=clock_timestamp()", "UPDATE job SET lease_until=clock_timestamp()-interval '1 second'", "UPDATE policy SET current_revision=current_revision+1"} {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			f := newPersonExecutionFixture(t)
			before := f.counts(t)
			f.exec(t, `CREATE FUNCTION person_follow_execution_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+mutation+`; RETURN NEW; END $$`)
			f.exec(t, `CREATE TRIGGER person_follow_execution_tail AFTER INSERT ON person_follow_execution FOR EACH ROW EXECUTE FUNCTION person_follow_execution_tail()`)
			if execution, err := f.scheduler.BeginExecution(t.Context(), f.lease); err == nil || execution != nil {
				if execution != nil {
					execution.Close()
				}
				t.Fatal("late mutation accepted execution")
			}
			var state string
			if err := f.p.QueryRow(t.Context(), `SELECT status FROM person_follow_sync_run`).Scan(&state); err != nil || state != "queued" {
				t.Fatal("rollback left a running run", err)
			}
			if !reflect.DeepEqual(f.executionCounts(t), []int{0, 0}) || !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("tail failure partially committed")
			}
			f.exec(t, `DROP TRIGGER person_follow_execution_tail ON person_follow_execution`)
			execution := f.begin(t)
			execution.Close()
		})
	}
}
func TestPersonFollowExecutionCancellationDuringAuthorizationPG(t *testing.T) {
	f := newPersonExecutionFixture(t)
	held, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err = held.Exec(t.Context(), `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	execution, err := f.scheduler.BeginExecution(ctx, f.lease)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || execution != nil {
		if execution != nil {
			execution.Close()
		}
		t.Fatal("authorization lock ignored cancellation", err)
	}
	if err = held.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A closed client socket may precede the server noticing disconnect after
	// the blocked authorization statement resumes; wait for the actual PG lock.
	deadline := time.Now().Add(3 * time.Second)
	for {
		ready, probeErr := f.scheduler.ExecutionReleased(t.Context(), f.lease.ID)
		if probeErr != nil {
			t.Fatal("cancelled execution lock observation failed", probeErr)
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled non-execution retained a physical gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reflect.DeepEqual(f.executionCounts(t), []int{0, 0}) {
		t.Fatal("cancelled authorization committed execution")
	}
	execution = f.begin(t)
	execution.Close()
}
func TestPersonFollowExecutionSchema124HistoryPG(t *testing.T) {
	f := newPersonExecutionFixture(t)
	if version, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history124); err != nil || version != 123 {
		t.Fatal("empty execution downgrade", version, err)
	}
	if execution, err := f.scheduler.BeginExecution(t.Context(), f.lease); !errors.Is(err, ports.ErrUnavailable) || execution != nil {
		if execution != nil {
			execution.Close()
		}
		t.Fatal("missing execution contract became valid", err)
	}
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), f.history124); err != nil || version != 124 {
		t.Fatal(version, err)
	}
	execution := f.begin(t)
	execution.Close()
	for _, query := range []string{`UPDATE person_follow_execution SET lease_token = lease_token + 1`, `UPDATE person_follow_execution SET released_at=NULL`, `UPDATE person_follow_execution SET released_at=clock_timestamp()`, `DELETE FROM person_follow_execution`, `TRUNCATE person_follow_execution`} {
		_, err := f.p.Exec(t.Context(), query)
		var storageError *pgconn.PgError
		if !errors.As(err, &storageError) || storageError.Code != "55000" {
			t.Fatal("execution history did not reject with its protection contract", err)
		}
	}
	if _, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history124); err == nil {
		t.Fatal("downgrade discarded release evidence")
	}
	if !reflect.DeepEqual(f.executionCounts(t), []int{1, 1}) {
		t.Fatal("history protection damaged execution evidence")
	}
}
