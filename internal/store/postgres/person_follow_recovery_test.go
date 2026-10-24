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
)

func personRecoverState(t *testing.T, f personSourceFixture) (string, string) {
	t.Helper()
	var job, run string
	if err := f.p.QueryRow(t.Context(), `SELECT j.state,r.status FROM job j JOIN person_follow_sync_page p ON p.job_id=j.id JOIN person_follow_sync_run r ON r.id=p.run_id WHERE j.id=$1`, f.lease.ID).Scan(&job, &run); err != nil {
		t.Fatal(err)
	}
	return job, run
}
func expirePersonJob(t *testing.T, f personSourceFixture) {
	t.Helper()
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.lease.ID)
}
func TestPersonFollowRecoveryOriginalJobWithoutExecutionPG(t *testing.T) {
	f := newPersonSourceFixture(t)
	before := f.counts(t)
	var payload []byte
	var due time.Time
	f.exec(t, `UPDATE job SET not_before=clock_timestamp()+interval '2 minutes' WHERE id=$1`, f.lease.ID)
	if err := f.p.QueryRow(t.Context(), `SELECT payload,not_before FROM job WHERE id=$1`, f.lease.ID).Scan(&payload, &due); err != nil {
		t.Fatal(err)
	}
	expirePersonJob(t, f)
	if err := f.scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, run := personRecoverState(t, f)
	if state != "queued" || run != "queued" {
		t.Fatal("safe original job not restored", state, run)
	}
	var exact bool
	if err := f.p.QueryRow(t.Context(), `SELECT payload=$2::jsonb AND not_before=$3::timestamptz AND lease_token=1 AND attempts=1 AND lease_owner IS NULL AND lease_until IS NULL FROM job WHERE id=$1`, f.lease.ID, payload, due).Scan(&exact); err != nil || !exact {
		t.Fatal("recovery rebound dispatch or shortened retry delay", err)
	}
	if !reflect.DeepEqual(before, f.counts(t)) || !reflect.DeepEqual(f.executionCounts(t), []int{0, 0}) {
		t.Fatal("recovering dispatch wrote business state")
	}
}
func TestPersonFollowRecoveryHeldAndReleasedSourcePG(t *testing.T) {
	f := newPersonSourceFixture(t)
	permit := f.acquire(t)
	if err := permit.StoreCredits(t.Context(), f.credits(1)); err != nil {
		t.Fatal(err)
	}
	original, err := permit.Items(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expirePersonJob(t, f)
	if err = f.scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, run := personRecoverState(t, f)
	if state != "running" || run != "running" {
		t.Fatal("logical timeout released active physical execution", state, run)
	}
	if !reflect.DeepEqual(f.executionCounts(t), []int{1, 0}) {
		t.Fatal("recovery invented release evidence")
	}
	permit.Close()
	if err = f.scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, run = personRecoverState(t, f)
	if state != "queued" || run != "running" {
		t.Fatal("released execution was not reclaimable", state, run)
	}
	f.exec(t, `UPDATE job SET state='running',lease_owner='person-worker',lease_token=2,lease_until=clock_timestamp()+interval '5 minutes',attempts=2 WHERE id=$1`, f.lease.ID)
	f.lease.Token = 2
	resumed := f.acquire(t)
	if !resumed.Snapshot().SourceStored {
		t.Fatal("new lease lost sealed source")
	}
	items, err := resumed.Items(t.Context())
	if err != nil || !reflect.DeepEqual(original, items) {
		t.Fatal("recovery changed source identity", err)
	}
	if !reflect.DeepEqual(f.sourceCounts(t), []int{1, 1, 0}) {
		t.Fatal("recovery duplicated source or candidate")
	}
}
func TestPersonFollowRecoveryUnknownSessionStaysManualPG(t *testing.T) {
	f := newPersonSourceFixture(t)
	permit := f.acquire(t)
	var pid int32
	if err := f.p.QueryRow(t.Context(), `SELECT backend_pid FROM person_follow_execution`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var killed bool
	if err := f.p.QueryRow(t.Context(), `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); err != nil || !killed {
		t.Fatal(err)
	}
	expirePersonJob(t, f)
	f.exec(t, `UPDATE person_follow_config SET enabled=false,version=version+1,updated_at=clock_timestamp()`)
	if err := f.scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	job, run := personRecoverState(t, f)
	if job != "dead" || run != "review_required" {
		t.Fatal("unknown physical result was cleared by deactivation", job, run)
	}
	permit.Close()
	if !reflect.DeepEqual(f.executionCounts(t), []int{1, 0}) {
		t.Fatal("lost session fabricated release")
	}
	if err := f.scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.scheduler.EnqueueDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.scheduleCounts(t), []int{1, 1, 1, 0}) {
		t.Fatal("manual boundary authorized another dispatch")
	}
}
func TestPersonFollowRecoveryInvalidQueuedAndMaxAttemptsPG(t *testing.T) {
	for _, scenario := range []string{"disabled", "exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPersonSourceFixture(t)
			before := f.counts(t)
			if scenario == "disabled" {
				f.exec(t, `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`, f.lease.ID)
				f.exec(t, `UPDATE person_follow_config SET enabled=false,version=version+1,updated_at=clock_timestamp()`)
			} else {
				f.exec(t, `UPDATE job SET state='dead',attempts=max_attempts,lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1`, f.lease.ID)
			}
			if err := f.scheduler.RecoverExpired(t.Context()); err != nil {
				t.Fatal(err)
			}
			job, run := personRecoverState(t, f)
			want := "aborted"
			if scenario == "exhausted" {
				want = "review_required"
			}
			if job != "dead" || run != want {
				t.Fatal("invalid recovery terminal state", job, run)
			}
			if !reflect.DeepEqual(before, f.counts(t)) || !reflect.DeepEqual(f.executionCounts(t), []int{0, 0}) {
				t.Fatal("recovery created unauthorized work")
			}
		})
	}
}
func TestPersonFollowRecoveryTailRollbackPG(t *testing.T) {
	for _, table := range []string{"job", "person_follow_sync_run"} {
		t.Run(table, func(t *testing.T) {
			f := newPersonSourceFixture(t)
			expirePersonJob(t, f)
			if table == "person_follow_sync_run" {
				f.exec(t, `UPDATE person_follow_config SET enabled=false,version=version+1,updated_at=clock_timestamp()`)
			}
			f.exec(t, `CREATE FUNCTION person_recovery_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned private recovery'; END $$`)
			f.exec(t, "CREATE TRIGGER person_recovery_tail BEFORE UPDATE ON "+table+" FOR EACH ROW EXECUTE FUNCTION person_recovery_tail()")
			before := f.counts(t)
			if err := f.scheduler.RecoverExpired(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("tail error not safely classified", err)
			}
			state, run := personRecoverState(t, f)
			if state != "running" || run != "queued" || !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("recovery partially committed", state, run)
			}
			f.exec(t, "DROP TRIGGER person_recovery_tail ON "+table)
			if err := f.scheduler.RecoverExpired(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestPersonFollowRecovery65PastHeldPagePG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), repositoryMigrationsThrough(t, postgres.CurrentMigrationVersion)); err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatal("recovery fixture migration", version, err)
	}
	for i := 0; i < 65; i++ {
		input := f.input()
		input.Settings.PersonID += int64(i)
		f.save(t, uuid.New(), input, fmt.Sprintf("recover-%d", i))
	}
	f.schedule(t)
	f.schedule(t)
	f.exec(t, `UPDATE job SET state='running',lease_owner='person-worker',lease_token=1,lease_until=clock_timestamp()-interval '1 second',attempts=1 WHERE kind='person_follow_sync'`)
	before := f.counts(t)
	held, err := f.p.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_, _ = held.Exec(cleanup, `SELECT pg_advisory_unlock_all()`)
		held.Release()
	}()
	if _, err = held.Exec(t.Context(), `SELECT pg_advisory_lock(pulse_person_follow_lock_key(config_id)) FROM (SELECT r.config_id FROM person_follow_sync_page p JOIN person_follow_sync_run r ON r.id=p.run_id ORDER BY p.job_id LIMIT 64) candidates`); err != nil {
		t.Fatal(err)
	}
	if err = f.scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	var queued, running int
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE state='queued'),count(*) FILTER(WHERE state='running') FROM job WHERE kind='person_follow_sync'`).Scan(&queued, &running); err != nil || queued != 1 || running != 64 {
		t.Fatal("held first page starved last task", queued, running, err)
	}
	if _, err = held.Exec(t.Context(), `SELECT pg_advisory_unlock_all()`); err != nil {
		t.Fatal(err)
	}
	if err = f.scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE state='queued'),count(*) FILTER(WHERE state='running') FROM job WHERE kind='person_follow_sync'`).Scan(&queued, &running); err != nil || queued != 65 || running != 0 {
		t.Fatal("recovery missed original tasks", queued, running, err)
	}
	if !reflect.DeepEqual(before, f.counts(t)) || !reflect.DeepEqual(f.scheduleCounts(t), []int{65, 65, 65, 0}) {
		t.Fatal("recovery pagination rebound tasks or business ledgers")
	}
	t.Log("M11_PERSON_RECOVERY_65_PASS first_page=PHYSICALLY_HELD later_task=NOT_STARVED tasks=ORIGINAL_65 physical_io=NOT_STARTED")
}
func TestPersonFollowRecoveryIsolationPG(t *testing.T) {
	f := newPersonSourceFixture(t)
	expirePersonJob(t, f)
	before := f.counts(t)
	f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=$1`, uuid.New())
	if err := f.scheduler.RecoverExpired(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("isolation ignored", err)
	}
	state, run := personRecoverState(t, f)
	if state != "running" || run != "queued" || !reflect.DeepEqual(before, f.counts(t)) {
		t.Fatal("isolated recovery changed protected state")
	}
}
