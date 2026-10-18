package transfer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func leaseTestWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("owned operation did not reach expected checkpoint")
	}
}

func TestTransferLeaseRenewsDuringSynchronousIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	renewed := make(chan struct{})
	done := make(chan error, 1)
	var finish sync.Once
	var calls atomic.Int32
	go func() {
		defer close(done)
		done <- withTransferLease(ctx, func(context.Context) error {
			if calls.Add(1) == 2 {
				close(renewed)
			}
			return nil
		}, func(context.Context) error { <-release; return nil }, time.Millisecond)
	}()
	t.Cleanup(func() { cancel(); finish.Do(func() { close(release) }); <-done })
	leaseTestWait(t, renewed)
	select {
	case err := <-done:
		t.Fatalf("returned while physical IO was blocked: %v", err)
	default:
	}
	finish.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 {
		t.Fatal("no renewal while operation was blocked")
	}
}

func TestTransferLeaseLossCancelsButWaitsForPhysicalIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	canceled := make(chan struct{})
	done := make(chan error, 1)
	var finish sync.Once
	var calls atomic.Int32
	lost := errors.New("owner token changed")
	go func() {
		defer close(done)
		done <- withTransferLease(ctx, func(context.Context) error {
			if calls.Add(1) > 1 {
				return lost
			}
			return nil
		}, func(c context.Context) error { <-c.Done(); close(canceled); <-release; return c.Err() }, time.Millisecond)
	}()
	t.Cleanup(func() { cancel(); finish.Do(func() { close(release) }); <-done })
	leaseTestWait(t, canceled)
	select {
	case err := <-done:
		t.Fatalf("lost authority released live IO: %v", err)
	default:
	}
	finish.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, lost) {
		t.Fatalf("renewal failure lost: %v", err)
	}
}

func TestTransferLeaseShutdownWaitsForUncancellableIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan error, 1)
	var finish sync.Once
	go func() {
		defer close(done)
		done <- withTransferLease(ctx, func(context.Context) error { return nil }, func(context.Context) error { close(started); <-release; return nil }, time.Millisecond)
	}()
	t.Cleanup(func() { cancel(); finish.Do(func() { close(release) }); <-done })
	leaseTestWait(t, started)
	cancel()
	select {
	case err := <-done:
		t.Fatalf("shutdown released live IO: %v", err)
	default:
	}
	finish.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown result: %v", err)
	}
}
