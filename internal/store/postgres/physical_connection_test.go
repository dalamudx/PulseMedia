package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestPhysicalConnectionAdmissionPreservesTransactions(t *testing.T) {
	pool, _, _, _ := interactiveFixture(t)
	held := make([]*postgres.PhysicalConnection, 0, 4)
	defer func() {
		for _, conn := range held {
			conn.Release()
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for range 4 {
		conn, err := postgres.AcquirePhysicalConnection(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	cancel()
	blocked, stop := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer stop()
	if conn, err := postgres.AcquirePhysicalConnection(blocked, pool); !errors.Is(err, context.DeadlineExceeded) {
		if conn != nil {
			conn.Release()
		}
		t.Fatalf("admission after cancellation: %v", err)
	}
	if got := pool.Stat().AcquiredConns(); got != 4 {
		t.Fatalf("waiting leaked connections: %d", got)
	}
	short, stopShort := context.WithTimeout(t.Context(), time.Second)
	defer stopShort()
	tx, err := pool.Begin(short)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(short, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(short); err != nil {
		t.Fatal(err)
	}
	held[0].Release()
	next, err := postgres.AcquirePhysicalConnection(short, pool)
	if err != nil {
		t.Fatal(err)
	}
	next.Release()
	// Close and Hijack must release admission even when a native outcome is unknown.
	raw := held[1].Hijack()
	if err = raw.Close(short); err != nil {
		t.Fatal(err)
	}
	held[1].Release()
	next, err = postgres.AcquirePhysicalConnection(short, pool)
	if err != nil {
		t.Fatal(err)
	}
	next.Release()
}

func TestPhysicalConnectionSearchAdmissionHasTransactionCapacity(t *testing.T) {
	pool, _, _, _ := interactiveFixture(t)
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for range 3 {
		release, err := postgres.AcquireIndexerPermit(t.Context(), pool, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	indexer := interactiveSite(t, pool, "admission", "online", true, false)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, permit, err := postgres.NewSearchExecutionStore(pool).AdmitQuery(ctx, ports.SearchQuerySpec{ID: uuid.New(), IndexerID: indexer, ConfigVersion: 1, Keyword: "capacity"})
	if err != nil {
		t.Fatal(err)
	}
	if permit == nil {
		t.Fatal("no execution permit")
	}
	permit.Close()
	if got := pool.Stat().AcquiredConns(); got != 3 {
		t.Fatalf("retained connections=%d", got)
	}
}
