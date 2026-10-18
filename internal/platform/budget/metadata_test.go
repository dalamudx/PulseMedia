package budget_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestMetadataBudgetCancellationDoesNotReleasePhysicalOwner(t *testing.T) {
	clk := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	b := budget.NewMetadataBudget(clk)
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, err := b.Acquire(owner)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for i := 0; i < 3; i++ {
		r, e := b.Acquire(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer r()
	}
	clk.Advance(time.Second)
	cancel()
	denied := func() {
		ctx, stop := context.WithTimeout(context.Background(), 15*time.Millisecond)
		defer stop()
		if r, e := b.Acquire(ctx); !errors.Is(e, context.DeadlineExceeded) {
			if r != nil {
				r()
			}
			t.Fatal("physical capacity changed", e)
		}
	}
	denied()
	release()
	replacement, err := b.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer replacement()
	release()
	denied()
}

func TestMetadataTokenBucketRefillAndSharedCooldown(t *testing.T) {
	clk := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	b := budget.NewMetadataBudget(clk)
	for i := 0; i < 4; i++ {
		r, e := b.Acquire(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		r()
	}
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer stop()
	if r, e := b.Acquire(ctx); !errors.Is(e, context.DeadlineExceeded) {
		if r != nil {
			r()
		}
		t.Fatal("fifth token bypassed bucket", e)
	}
	clk.Advance(250 * time.Millisecond)
	r, e := b.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	r()
	b.RecordRateLimit(10 * time.Second)
	b.RecordRateLimit(time.Second)
	if r, e := b.Acquire(context.Background()); e == nil {
		r()
		t.Fatal("cooldown bypassed")
	} else if rate, ok := ports.IsRateLimited(e); !ok || rate.RetryAfter != 10*time.Second {
		t.Fatal("cooldown was shortened", e)
	}
	independent := budget.NewMetadataBudget(clk)
	r, e = independent.Acquire(context.Background())
	if e != nil {
		t.Fatal("unrelated budget blocked", e)
	}
	r()
	clk.Advance(10 * time.Second)
	for i := 0; i < 4; i++ {
		r, e := b.Acquire(context.Background())
		if e != nil {
			t.Fatal("cooldown did not recover", e)
		}
		r()
	}
}
