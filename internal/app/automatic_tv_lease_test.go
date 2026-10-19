package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutomaticTVLeaseJoinsPhysicalExecution(t *testing.T) {
	for _, scenario := range []string{"shutdown", "lost-lease", "budget"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			entered, cancelled := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			budget, interval := time.Second, time.Hour
			if scenario == "lost-lease" {
				interval = time.Millisecond
			}
			if scenario == "budget" {
				budget = 5 * time.Millisecond
			}
			var renewals atomic.Int32
			go func() {
				done <- runAutomaticTVLeased(ctx, budget, interval, func(context.Context) error {
					renewals.Add(1)
					return errors.New("lease lost")
				}, func(work context.Context) error {
					close(entered)
					<-work.Done()
					close(cancelled)
					<-release
					return nil
				})
			}()
			joined := false
			t.Cleanup(func() {
				cancel()
				unblock()
				if !joined {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("physical execution not joined")
					}
				}
			})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("executor did not enter")
			}
			if scenario == "shutdown" {
				cancel()
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("executor context not cancelled")
			}
			select {
			case err := <-done:
				joined = true
				t.Fatalf("physical work detached: %v", err)
			default:
			}
			if scenario == "lost-lease" && renewals.Load() != 1 {
				t.Fatal("lost heartbeat continued renewal")
			}
			unblock()
			select {
			case err := <-done:
				joined = true
				want := context.Canceled
				if scenario == "budget" {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("execution cancellation=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("executor failed to join")
			}
		})
	}
}

func TestAutomaticTVLeaseJoinsHeartbeatAndPreservesResult(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	renewing, renewalStopped := make(chan struct{}), make(chan struct{})
	failure := errors.New("execution failed")
	err := runAutomaticTVLeased(ctx, time.Second, time.Millisecond, func(heartbeat context.Context) error {
		close(renewing)
		<-heartbeat.Done()
		close(renewalStopped)
		return heartbeat.Err()
	}, func(context.Context) error {
		<-renewing
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal("executor failure changed")
	}
	select {
	case <-renewalStopped:
	default:
		t.Fatal("heartbeat outlived executor")
	}
	if err = runAutomaticTVLeased(ctx, time.Second, time.Hour, func(context.Context) error { t.Error("unexpected renewal"); return nil }, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
