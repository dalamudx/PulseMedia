package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMediaSidecarPhysicalWaitsForActualReturn(t *testing.T) {
	for _, mode := range []string{"shutdown", "lease_loss"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			started, canceled, physicalRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
			finished := make(chan struct{})
			result := make(chan error, 1)
			var once sync.Once
			leaseErr := errors.New("lease lost")
			t.Cleanup(func() {
				cancel()
				once.Do(func() { close(physicalRelease) })
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("owned physical call did not finish")
				}
			})
			go func() {
				defer close(finished)
				result <- runMediaSidecarPhysical(ctx, func(context.Context) error {
					if mode == "lease_loss" {
						return leaseErr
					}
					return nil
				}, func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					close(canceled)
					// Simulate an OS/file call that ignores cancellation and still owns
					// the physical root slot. Only its actual return releases that slot.
					<-physicalRelease
					return ctx.Err()
				}, 10*time.Millisecond)
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("physical call never started")
			}
			if mode == "shutdown" {
				cancel()
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("logical cancellation never reached work")
			}
			select {
			case err := <-result:
				t.Fatalf("physical ownership released early: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			once.Do(func() { close(physicalRelease) })
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) || (mode == "lease_loss" && !errors.Is(err, leaseErr)) {
					t.Fatalf("lost cancellation/lease evidence: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("completed physical work was not joined")
			}
		})
	}
}

func TestMediaSidecarPhysicalSuccess(t *testing.T) {
	renewed := false
	physicalReturned := false
	err := runMediaSidecarPhysical(t.Context(), func(context.Context) error {
		renewed = true
		return nil
	}, func(context.Context) error {
		physicalReturned = true
		return nil
	}, time.Hour)
	if err != nil || renewed || !physicalReturned {
		t.Fatalf("physical completion was not joined: err=%v renewed=%v returned=%v", err, renewed, physicalReturned)
	}
}
