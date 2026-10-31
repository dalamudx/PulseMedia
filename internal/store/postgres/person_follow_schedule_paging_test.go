package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func seedPersonFollowFailedPage(t *testing.T, f personScheduleFixture) {
	t.Helper()
	for i := 1; i <= 66; i++ {
		in := f.input()
		in.Settings.PersonID += int64(i)
		f.save(t, uuid.MustParse(fmt.Sprintf("10000000-0000-0000-0000-%012d", i)), in, fmt.Sprintf("failed-page-%d", i))
	}
	f.exec(t, `DELETE FROM idempotency_record WHERE idempotency_key IN(SELECT command_key FROM person_follow_config_history WHERE config_id<'10000000-0000-0000-0000-000000000065'::uuid)`)
}

func TestPersonFollowScheduleAdvancesPastFailedPagePG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	seedPersonFollowFailedPage(t, f)
	if err := f.scheduler.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrPersonFollowConflict) {
		t.Fatal("bad page lost error", err)
	}
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{0, 0, 0, 0}) {
		t.Fatal("batch exceeded 64 or failed page committed", got)
	}
	// Restart only the scanner; the database remains the authority for queued work.
	f.scheduler = postgres.NewPersonFollowSyncStore(f.p, idgen.New())
	if err := f.scheduler.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrPersonFollowConflict) {
		t.Fatal("restart skipped original error", err)
	}
	f.schedule(t)
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{2, 2, 2, 0}) {
		t.Fatal("failed prefix starved healthy next page", got)
	}
	in := f.input()
	in.Settings.PersonID = 2000
	f.save(t, uuid.MustParse("00000000-0000-0000-0000-000000000001"), in, "new-before-cursor")
	for range 2 {
		if err := f.scheduler.EnqueueDue(t.Context()); err != nil && !errors.Is(err, ports.ErrPersonFollowConflict) {
			t.Fatal(err)
		}
	}
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{3, 3, 3, 0}) {
		t.Fatal("wraparound missed new prefix or duplicated work", got)
	}
	var failed int
	if err := f.p.QueryRow(t.Context(), `SELECT count(*) FROM person_follow_sync_run WHERE config_id>='10000000-0000-0000-0000-000000000001' AND config_id<'10000000-0000-0000-0000-000000000065'`).Scan(&failed); err != nil || failed != 0 {
		t.Fatal("failed prefix partly committed", failed, err)
	}
}

func TestPersonFollowScheduleConcurrentCursorPG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	seedPersonFollowFailedPage(t, f)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.scheduler.EnqueueDue(t.Context()) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, ports.ErrPersonFollowConflict) {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := f.scheduler.EnqueueDue(t.Context()); err != nil && !errors.Is(err, ports.ErrPersonFollowConflict) {
			t.Fatal(err)
		}
	}
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{2, 2, 2, 0}) {
		t.Fatal("concurrent cursor lost or duplicated healthy work", got)
	}
}

func TestPersonFollowScheduleBusyScannerDoesNotBlockCallerPG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	ids := seedPersonScheduleIsolation(t, f)
	blocker, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM person_follow_config WHERE id=$1 FOR UPDATE`, ids[0]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() { defer close(done); result <- f.scheduler.EnqueueDue(ctx) }()
	defer func() { cancel(); _ = blocker.Rollback(context.Background()); <-done }()
	waitPreparationBlock(t, f.p, blocker.Conn().PgConn().PID())
	busy, busyCancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer busyCancel()
	if err = f.scheduler.EnqueueDue(busy); err != nil {
		t.Fatal("busy scanner did not yield", err)
	}
	stopped, stop := context.WithCancel(t.Context())
	stop()
	if err = f.scheduler.EnqueueDue(stopped); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled busy caller lost cancellation", err)
	}
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("active scanner lost cancellation", err)
	}
	if err = blocker.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.schedule(t)
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{3, 3, 3, 0}) {
		t.Fatal("cancelled cursor skipped unfinished work", got)
	}
}
