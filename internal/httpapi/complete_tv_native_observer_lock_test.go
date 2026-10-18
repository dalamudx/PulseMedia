package httpapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestCompleteTVNativeObserverNativePGNativeIOExcludesManagersAndSender(t *testing.T) {
	n := newNativeObserverFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	n.hook = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, e := n.observe(context.Background()); done <- e }()
	defer func() {
		unblock()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(15 * time.Second):
			t.Error("observer not joined")
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("HTTP not entered")
	}
	other := n.newManager()
	if r, e := other.ObserveCompleteTV(context.Background(), n.observer, n.request); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("parallel observer admitted", r, e)
	}
	if r, e := other.SendCompleteTV(context.Background(), n.c.Lease, n.request.OperationID); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("parallel sender admitted", r, e)
	}
	n.wire(t, 1)
}

func TestCompleteTVNativeObserverNativePGSenderExcludesObservation(t *testing.T) {
	s := newByteSenderFixture(t, completeTVRaw)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	s.fetchHook = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, e := s.send(context.Background()); done <- e }()
	defer func() {
		unblock()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(15 * time.Second):
			t.Error("sender not joined")
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("sender not entered")
	}
	// The observer cannot even load its snapshot while the sender is physically
	// executing, before preparation has created any task row.
	repo := &unreachableObservationRepository{t: t}
	req := ports.CompleteTVObservationRequest{TaskID: uuid.New(), OperationID: s.c.OperationID, Lease: ports.JobLease{ID: uuid.New(), Owner: "observer", Token: 1}}
	if r, e := s.newManager().ObserveCompleteTV(context.Background(), repo, req); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("observer admitted during send", r, e)
	}
	if s.fetches.Load() != 1 || s.adds.Load() != 0 {
		t.Fatal("unexpected Add while blocked")
	}
}

type unreachableObservationRepository struct{ t *testing.T }

func (r *unreachableObservationRepository) LoadCompleteTVObservation(context.Context, ports.CompleteTVObservationRequest) (*ports.CompleteTVObservationSnapshot, error) {
	r.t.Error("load bypassed physical lock")
	return nil, ports.ErrUnavailable
}
func (r *unreachableObservationRepository) CommitCompleteTVObservation(context.Context, ports.CompleteTVObservationSnapshot, ports.CompleteTVRemoteObservation) (*ports.CompleteTVObservationReceipt, error) {
	r.t.Error("unexpected commit")
	return nil, ports.ErrUnavailable
}

func TestCompleteTVNativeObserverNativePGBudgetAndLeaseThroughCommit(t *testing.T) {
	n := newNativeObserverFixture(t)
	s := n.load(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	barrier := &observationBarrier{n.observer, entered, release}
	done := make(chan error, 1)
	go func() { _, e := n.manager.ObserveCompleteTV(context.Background(), barrier, n.request); done <- e }()
	defer func() {
		unblock()
		select {
		case e := <-done:
			if !errors.Is(e, ports.ErrCompleteTVObservationStale) {
				t.Error("expired final lease accepted", e)
			}
		case <-time.After(15 * time.Second):
			t.Error("observer not joined")
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no commit")
	}
	// HTTP has already returned. The observer must still occupy its shared slot
	// while the bounded fact writeback is blocked.
	c := &observerBlockingClient{testEntered: make(chan struct{}, 2), release: release}
	n.manager.RegisterClient(s.ClientID, c)
	testDone := make(chan error, 1)
	go func() { _, _, e := n.manager.TestClient(context.Background(), s.ClientID); testDone <- e }()
	defer func() {
		unblock()
		select {
		case e := <-testDone:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("client test not joined")
		}
	}()
	select {
	case <-c.testEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("no second slot")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, e := n.manager.TestClient(ctx, s.ClientID); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("budget released before commit", e)
	}
	n.exec(t, `UPDATE job SET lease_until=clock_timestamp() WHERE id=$1`, n.request.Lease.ID)
	// Take the full baseline after intentional lease expiry. Returning physical
	// facts must not permit a stale observation lease to write anything else.
	before := n.snapshot(t)
	unblock()
	select {
	case e := <-done:
		if !errors.Is(e, ports.ErrCompleteTVObservationStale) {
			t.Fatal(e)
		}
		done <- ports.ErrCompleteTVObservationStale
	case <-time.After(10 * time.Second):
		t.Fatal("commit not returned")
	}
	if n.snapshot(t) != before {
		t.Fatal("expired lease changed facts/protection")
	}
	n.wire(t, 1)
}
