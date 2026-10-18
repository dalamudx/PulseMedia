package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
	"time"
)

type admissionProbeClock struct{ now func() time.Time }

func (c admissionProbeClock) Now() time.Time                  { return c.now() }
func (c admissionProbeClock) Since(t time.Time) time.Duration { return c.now().Sub(t) }

func TestManualSearchRoundReadsInjectedClockUnderOriginalLocks(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	expected := time.Date(2030, 4, 5, 6, 7, 8, 0, time.UTC)
	calls := 0
	var lockError error
	clock := admissionProbeClock{now: func() time.Time {
		calls++
		// This separate connection must already encounter the admission's root lock.
		_, lockError = f.pool.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE NOWAIT", f.root)
		return expected
	}}
	coordinator := search.NewRoundCoordinator(postgres.NewSearchRoundStore(f.pool, idgen.New()), clock, nil)
	round, err := coordinator.StartRound(ctx, f.wanted, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var pgError *pgconn.PgError
	if calls != 1 || !errors.As(lockError, &pgError) || pgError.Code != "55P03" {
		t.Fatal("manual clock read before admission locks", calls, lockError)
	}
	if !round.Deadline.Equal(expected.Add(20 * time.Second)) {
		t.Fatal("manual deadline lost injected clock", round.Deadline)
	}
}
