package operation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/operation"
	"github.com/google/uuid"
)

type timelineReaderFunc func(context.Context, *ports.Actor, uuid.UUID, ports.OperationTimelineRequest) (*ports.OperationTimelinePage, error)

func (f timelineReaderFunc) ReadOperationTimeline(ctx context.Context, a *ports.Actor, id uuid.UUID, r ports.OperationTimelineRequest) (*ports.OperationTimelinePage, error) {
	return f(ctx, a, id, r)
}

func TestTimelineServiceRead(t *testing.T) {
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	id := uuid.New()
	want := &ports.OperationTimelinePage{OperationID: id, Items: []ports.OperationTimelineEntry{}}
	calls := 0
	reader := timelineReaderFunc(func(ctx context.Context, a *ports.Actor, got uuid.UUID, r ports.OperationTimelineRequest) (*ports.OperationTimelinePage, error) {
		calls++
		if a != actor || got != id || r.Limit != 50 || r.Cursor != "opaque" {
			t.Fatalf("reader arguments: %+v %s %+v", a, got, r)
		}
		return want, nil
	})
	service := operation.NewTimelineService(reader)
	got, err := service.Read(t.Context(), actor, id, ports.OperationTimelineRequest{Cursor: "opaque"})
	if got != want || err != nil || calls != 1 {
		t.Fatalf("read %+v %v calls=%d", got, err, calls)
	}
	for _, tc := range []struct {
		name    string
		actor   *ports.Actor
		id      uuid.UUID
		request ports.OperationTimelineRequest
		want    error
	}{
		{"nil actor", nil, id, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: actor.ID, Role: ports.RoleAdmin}, id, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"empty id", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleMember}, id, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"role", &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: "guest"}, id, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"empty scopes", &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleMember, Scopes: []string{}}, id, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"write scope", &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleMember, Scopes: []string{"write"}}, id, ports.OperationTimelineRequest{}, ports.ErrForbidden},
		{"nil operation", actor, uuid.Nil, ports.OperationTimelineRequest{}, ports.ErrInvalidOperationQuery},
		{"negative limit", actor, id, ports.OperationTimelineRequest{Limit: -1}, ports.ErrInvalidOperationQuery},
		{"large limit", actor, id, ports.OperationTimelineRequest{Limit: 101}, ports.ErrInvalidOperationQuery},
		{"large cursor", actor, id, ports.OperationTimelineRequest{Cursor: strings.Repeat("x", 1025)}, ports.ErrInvalidOperationQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := service.Read(t.Context(), tc.actor, tc.id, tc.request)
			if got != nil || !errors.Is(err, tc.want) || calls != 1 {
				t.Fatalf("read %+v %v calls=%d", got, err, calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := service.Read(ctx, actor, id, ports.OperationTimelineRequest{}); got != nil || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancel %+v %v calls=%d", got, err, calls)
	}
}

func TestTimelineServiceReaderFailures(t *testing.T) {
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin, Scopes: []string{"read"}}
	failure := errors.New("read failed")
	for _, tc := range []struct {
		name   string
		reader ports.OperationTimelineReader
		want   error
	}{
		{"missing", nil, ports.ErrUnavailable},
		{"nil page", timelineReaderFunc(func(context.Context, *ports.Actor, uuid.UUID, ports.OperationTimelineRequest) (*ports.OperationTimelinePage, error) {
			return nil, nil
		}), ports.ErrUnavailable},
		{"read failure", timelineReaderFunc(func(context.Context, *ports.Actor, uuid.UUID, ports.OperationTimelineRequest) (*ports.OperationTimelinePage, error) {
			return nil, failure
		}), failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := operation.NewTimelineService(tc.reader).Read(t.Context(), actor, uuid.New(), ports.OperationTimelineRequest{Limit: 100})
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("read %+v %v", got, err)
			}
		})
	}
	var service *operation.TimelineService
	if got, err := service.Read(t.Context(), actor, uuid.New(), ports.OperationTimelineRequest{}); got != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("nil service %+v %v", got, err)
	}
}
