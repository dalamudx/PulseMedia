package budget_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
)

func TestBudgetSharedAndEndpointIsolation(t *testing.T) {
	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	mgr := budget.NewManager(100*time.Millisecond, mockClock)
	ctx := context.Background()

	endpointA := "indexer-a"
	endpointB := "indexer-b"

	// 1. Acquire endpoint A succeeds
	if err := mgr.Acquire(ctx, endpointA); err != nil {
		t.Fatalf("acquire A failed: %v", err)
	}

	// 2. Put endpoint A into cooldown (simulating 429)
	mgr.RecordRateLimit(endpointA, 10*time.Second)

	// Endpoint A should be rejected with ErrEndpointRateLimited
	err := mgr.Acquire(ctx, endpointA)
	if !errors.Is(err, budget.ErrEndpointRateLimited) {
		t.Fatalf("expected ErrEndpointRateLimited on A, got: %v", err)
	}

	// 3. Endpoint B must NOT be blocked! (One slow/429 endpoint does NOT block others)
	if err := mgr.Acquire(ctx, endpointB); err != nil {
		t.Fatalf("endpoint B should acquire cleanly while A is rate limited: %v", err)
	}

	// 4. Advance clock past cooldown: endpoint A recovers!
	mockClock.Advance(15 * time.Second)
	if err := mgr.Acquire(ctx, endpointA); err != nil {
		t.Fatalf("endpoint A should recover after cooldown: %v", err)
	}
}
