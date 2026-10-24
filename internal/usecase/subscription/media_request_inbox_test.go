package subscription

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"math"
	"testing"
	"time"
)

type inboxTestReader struct {
	ports.MediaRequestRepository
	count   int
	summary *ports.MediaRequestInboxSummary
	err     error
}

func (f *inboxTestReader) ReadMediaRequestInbox(context.Context, *ports.Actor) (*ports.MediaRequestInboxSummary, error) {
	f.count++
	return f.summary, f.err
}
func (f *inboxTestReader) ListPendingMediaRequests(context.Context, *ports.Actor, int, *uuid.UUID) ([]ports.MediaRequest, error) {
	f.count++
	return []ports.MediaRequest{}, f.err
}
func inboxActor() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
}

func TestMediaRequestInboxServiceAdmission(t *testing.T) {
	f := &inboxTestReader{}
	s := NewMediaRequestService(f, nil)
	ctx := t.Context()
	if _, err := s.Summary(ctx, nil); !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatal(err)
	}
	a := inboxActor()
	a.Role = ports.RoleMember
	if _, err := s.Summary(ctx, a); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.Pending(ctx, a, 25, nil); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal(err)
	}
	a = inboxActor()
	a.Scopes = []string{}
	if _, err := s.Summary(ctx, a); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("token scope", err)
	}
	if f.count != 0 {
		t.Fatal("admission contacted storage")
	}
}
func TestMediaRequestInboxServiceCountAndFailure(t *testing.T) {
	f := &inboxTestReader{}
	s := NewMediaRequestService(f, nil)
	ctx := t.Context()
	a := inboxActor()
	for _, v := range []*ports.MediaRequestInboxSummary{nil, {PendingCount: -1, ObservedAt: time.Now()}, {PendingCount: 0}} {
		f.summary = v
		if _, err := s.Summary(ctx, a); !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("bad observation", err)
		}
	}
	for _, n := range []int64{0, math.MaxInt64} {
		f.summary = &ports.MediaRequestInboxSummary{PendingCount: n, ObservedAt: time.Now()}
		v, err := s.Summary(ctx, a)
		if err != nil || v.PendingCount != n {
			t.Fatal("exact count", v, err)
		}
	}
	f.err = errors.New("private storage failure")
	if _, err := s.Summary(ctx, a); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("safe error", err)
	}
	if _, err := s.Pending(ctx, a, 25, nil); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestMediaRequestInboxServiceBoundsAndCancellation(t *testing.T) {
	f := &inboxTestReader{}
	s := NewMediaRequestService(f, nil)
	a := inboxActor()
	ctx := t.Context()
	for _, n := range []int{0, -1, 101} {
		if _, err := s.Pending(ctx, a, n, nil); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("limit", n, err)
		}
	}
	zero := uuid.Nil
	if _, err := s.Pending(ctx, a, 25, &zero); !errors.Is(err, ports.ErrRejected) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Summary(cancelled, a); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Pending(cancelled, a, 25, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.count != 0 {
		t.Fatal("invalid read contacted storage")
	}
}
