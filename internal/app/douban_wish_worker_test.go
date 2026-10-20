package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDoubanWishPhysicalWaitsForExecution(t *testing.T) {
	for _, mode := range []string{"shutdown", "deadline", "renew_loss"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
				defer stop()
			}
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			returned := make(chan error, 1)
			finished := make(chan struct{})
			loss := errors.New("lease lost")
			physicalErr := errors.New("physical execution ended")
			go func() {
				defer close(finished)
				returned <- runDoubanWishPhysical(ctx, func(context.Context) error {
					if mode == "renew_loss" {
						return loss
					}
					return nil
				}, func(workCtx context.Context) error {
					close(started)
					<-workCtx.Done()
					close(canceled)
					<-release // Deliberately ignore cancellation until the physical call returns.
					return physicalErr
				}, time.Millisecond)
			}()
			released := false
			defer func() {
				cancel()
				if !released {
					close(release)
				}
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("owned execution did not drain")
				}
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("execution did not start")
			}
			if mode == "shutdown" {
				cancel()
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("execution was not canceled")
			}
			select {
			case err := <-returned:
				t.Fatalf("returned before physical completion: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			released = true
			select {
			case err := <-returned:
				if !errors.Is(err, physicalErr) {
					t.Fatalf("physical error lost: %v", err)
				}
				expected := error(context.Canceled)
				if mode == "deadline" {
					expected = context.DeadlineExceeded
				}
				if mode == "renew_loss" {
					expected = loss
				}
				if !errors.Is(err, expected) {
					t.Fatalf("cancellation or renewal error lost: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("execution did not drain after release")
			}
		})
	}
}

func TestDoubanWishPhysicalPageBudget(t *testing.T) {
	var observed context.Context
	err := runDoubanWishPhysical(context.Background(), func(context.Context) error {
		t.Error("unexpected renewal before immediate completion")
		return nil
	}, func(ctx context.Context) error {
		observed = ctx
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 14*time.Minute || time.Until(deadline) > 15*time.Minute {
			t.Error("page must receive its own 15 minute deadline")
		}
		return nil
	}, doubanWishHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Err() != context.Canceled {
		t.Fatal("completed child context was not canceled")
	}
	if doubanWishLease != 2*time.Minute || doubanWishHeartbeat != 15*time.Second {
		t.Fatal("unexpected job lease or renewal interval")
	}
}
