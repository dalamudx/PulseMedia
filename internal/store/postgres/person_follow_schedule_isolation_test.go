package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func seedPersonScheduleIsolation(t *testing.T, f personScheduleFixture) []uuid.UUID {
	t.Helper()
	ids := []uuid.UUID{uuid.MustParse("10000000-0000-0000-0000-000000000001"), uuid.MustParse("10000000-0000-0000-0000-000000000002"), uuid.MustParse("10000000-0000-0000-0000-000000000003")}
	for i, id := range ids {
		in := f.input()
		in.Settings.PersonID += int64(i)
		f.save(t, id, in, fmt.Sprintf("isolation-%d", i))
	}
	return ids
}

func TestPersonFollowScheduleFailureIsolationPG(t *testing.T) {
	for _, mode := range []string{"missing_receipt", "tail_write"} {
		t.Run(mode, func(t *testing.T) {
			f := newPersonScheduleFixture(t)
			ids := seedPersonScheduleIsolation(t, f)
			wantErr := ports.ErrUnavailable
			if mode == "missing_receipt" {
				f.exec(t, `DELETE FROM idempotency_record WHERE idempotency_key='person.follow.config:isolation-0'`)
				wantErr = ports.ErrPersonFollowConflict
			} else {
				f.exec(t, `CREATE FUNCTION person_follow_isolation_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS(SELECT 1 FROM person_follow_sync_run WHERE id=NEW.run_id AND config_id='`+ids[0].String()+`'::uuid) THEN RAISE EXCEPTION 'private injected schedule failure'; END IF; RETURN NEW; END $$`)
				f.exec(t, `CREATE TRIGGER person_follow_isolation_fault BEFORE INSERT ON person_follow_sync_page FOR EACH ROW EXECUTE FUNCTION person_follow_isolation_fault()`)
			}
			before := f.counts(t)
			for range 2 {
				if err := f.scheduler.EnqueueDue(t.Context()); !errors.Is(err, wantErr) {
					t.Fatal("failure hidden or misclassified", err)
				}
				if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{2, 2, 2, 0}) {
					t.Fatal("healthy configurations blocked or duplicated", got)
				}
			}
			var failedRuns int
			if err := f.p.QueryRow(t.Context(), `SELECT count(*) FROM person_follow_sync_run WHERE config_id=$1`, ids[0]).Scan(&failedRuns); err != nil || failedRuns != 0 {
				t.Fatal("failed configuration partly committed", failedRuns, err)
			}
			after := f.counts(t)
			for i := range before {
				if i != 5 && before[i] != after[i] {
					t.Fatal("scheduling changed protected business state", i)
				}
			}
			if mode == "missing_receipt" {
				in := f.input()
				in.ExpectedVersion = 1
				f.save(t, ids[0], in, "isolation-repaired")
			} else {
				f.exec(t, `DROP TRIGGER person_follow_isolation_fault ON person_follow_sync_page`)
			}
			f.schedule(t)
			if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{3, 3, 3, 0}) {
				t.Fatal("repair did not schedule only failed configuration", got)
			}
		})
	}
}

func TestPersonFollowScheduleCancellationPreservesCommittedConfigsPG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	ids := seedPersonScheduleIsolation(t, f)
	blocker, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM person_follow_config WHERE id=$1 FOR UPDATE`, ids[1]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() { defer close(done); result <- f.scheduler.EnqueueDue(ctx) }()
	defer func() { cancel(); _ = blocker.Rollback(context.Background()); <-done }()
	waitPreparationBlock(t, f.p, blocker.Conn().PgConn().PID())
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancel lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not cancel")
	}
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{1, 1, 1, 0}) {
		t.Fatal("cancel rolled back committed peer or continued scheduling", got)
	}
	if err = blocker.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.schedule(t)
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{3, 3, 3, 0}) {
		t.Fatal("restart duplicated or lost work", got)
	}
}
