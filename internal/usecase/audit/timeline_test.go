package audit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/auditlog"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/audit"
	"github.com/google/uuid"
)

type timelineRead func(context.Context, *ports.Actor, auditlog.Filter, *auditlog.Position, int) ([]ports.AuditSummary, error)

func (f timelineRead) ReadAuditTimeline(ctx context.Context, a *ports.Actor, q auditlog.Filter, p *auditlog.Position, n int) ([]ports.AuditSummary, error) {
	return f(ctx, a, q, p, n)
}
func timelineActor() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: "00000000-0000-4000-8000-000000000001", Role: ports.RoleAdmin}
}
func timelineRow(n byte) ports.AuditSummary {
	id := uuid.MustParse("00000000-0000-4000-8000-000000000000")
	id[15] = n
	return ports.AuditSummary{ID: id, CreatedAt: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC), ActorType: "user", ActorID: timelineActor().ID, Action: "subscription.create", TargetType: "subscription", TargetID: id.String()}
}

func TestAuditTimelineAuthorizationAndInputBeforeStorage(t *testing.T) {
	calls := 0
	reader := timelineRead(func(context.Context, *ports.Actor, auditlog.Filter, *auditlog.Position, int) ([]ports.AuditSummary, error) {
		calls++
		return nil, nil
	})
	svc := audit.NewTimelineService(reader)
	cases := []struct {
		name    string
		actor   *ports.Actor
		request ports.AuditTimelineRequest
		want    error
	}{
		{name: "missing", want: ports.ErrUnauthorized},
		{name: "member", actor: &ports.Actor{Type: ports.ActorTypeUser, ID: timelineActor().ID, Role: ports.RoleMember}, want: ports.ErrForbidden},
		{name: "system", actor: &ports.Actor{Type: ports.ActorTypeSystem, ID: timelineActor().ID, Role: ports.RoleAdmin}, want: ports.ErrForbidden},
		{name: "empty_scope", actor: &ports.Actor{Type: ports.ActorTypeUser, ID: timelineActor().ID, Role: ports.RoleAdmin, Scopes: []string{}}, want: ports.ErrForbidden},
		{name: "elevated_scope", actor: &ports.Actor{Type: ports.ActorTypeUser, ID: timelineActor().ID, Role: ports.RoleAdmin, Scopes: []string{"admin"}}, want: ports.ErrForbidden},
		{name: "bad_identity", actor: &ports.Actor{Type: ports.ActorTypeUser, ID: "not-an-id", Role: ports.RoleAdmin}, want: ports.ErrForbidden},
		{name: "negative", actor: timelineActor(), request: ports.AuditTimelineRequest{Limit: -1}, want: ports.ErrRejected},
		{name: "too_many", actor: timelineActor(), request: ports.AuditTimelineRequest{Limit: 201}, want: ports.ErrRejected},
		{name: "filter", actor: timelineActor(), request: ports.AuditTimelineRequest{Filter: auditlog.Filter{ActorType: "invalid"}}, want: ports.ErrRejected},
		{name: "cursor", actor: timelineActor(), request: ports.AuditTimelineRequest{Cursor: "broken"}, want: ports.ErrRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := svc.List(context.Background(), tc.actor, tc.request)
			if page != nil || !errors.Is(err, tc.want) || calls != 0 {
				t.Fatalf("unexpected storage access or result: %+v %v %d", page, err, calls)
			}
		})
	}
}

func TestAuditTimelineStableContinuationAndIsolation(t *testing.T) {
	rows := []ports.AuditSummary{timelineRow(9), timelineRow(8), timelineRow(7), timelineRow(6)}
	calls := 0
	reader := timelineRead(func(_ context.Context, a *ports.Actor, _ auditlog.Filter, before *auditlog.Position, limit int) ([]ports.AuditSummary, error) {
		calls++
		a.Scopes[0] = "mutated"
		if limit != 3 {
			t.Fatalf("expected extra-row lookahead: %d", limit)
		}
		var result []ports.AuditSummary
		for _, row := range rows {
			p := auditlog.Position{ID: row.ID, CreatedAt: row.CreatedAt}
			if before == nil || p.Before(*before) {
				result = append(result, row)
				if len(result) == limit {
					break
				}
			}
		}
		return result, nil
	})
	svc := audit.NewTimelineService(reader)
	actor := timelineActor()
	actor.Scopes = []string{"read"}
	first, err := svc.List(context.Background(), actor, ports.AuditTimelineRequest{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	rows = append([]ports.AuditSummary{timelineRow(10)}, rows...)
	second, err := svc.List(context.Background(), actor, ports.AuditTimelineRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 2 || second.NextCursor != "" || second.Items[0].ID != timelineRow(7).ID || second.Items[1].ID != timelineRow(6).ID {
		t.Fatalf("continued page: %+v %v", second, err)
	}
	first.Items[0].Action = "changed"
	if rows[1].Action == "changed" || actor.Scopes[0] != "read" || calls != 2 {
		t.Fatal("reader aliases escaped")
	}
	for _, limit := range []int{0, 200} {
		empty := audit.NewTimelineService(timelineRead(func(_ context.Context, _ *ports.Actor, _ auditlog.Filter, _ *auditlog.Position, n int) ([]ports.AuditSummary, error) {
			expected := limit + 1
			if limit == 0 {
				expected = 51
			}
			if n != expected {
				t.Fatalf("lookahead: %d", n)
			}
			return nil, nil
		}))
		page, err := empty.List(context.Background(), timelineActor(), ports.AuditTimelineRequest{Limit: limit})
		if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
			t.Fatalf("empty list: %+v %v", page, err)
		}
	}
}

func TestAuditTimelineFailureAndCorruptRows(t *testing.T) {
	for _, tc := range []struct {
		rows []ports.AuditSummary
		err  error
		want error
	}{
		{err: errors.New("private storage details"), want: ports.ErrUnavailable},
		{err: ports.ErrForbidden, want: ports.ErrForbidden},
		{rows: []ports.AuditSummary{timelineRow(9), timelineRow(9)}, want: ports.ErrUnavailable},
		{rows: []ports.AuditSummary{timelineRow(8), timelineRow(9)}, want: ports.ErrUnavailable},
		{rows: []ports.AuditSummary{{}}, want: ports.ErrUnavailable},
		{rows: []ports.AuditSummary{timelineRow(9), timelineRow(8), timelineRow(7)}, want: ports.ErrUnavailable},
	} {
		svc := audit.NewTimelineService(timelineRead(func(context.Context, *ports.Actor, auditlog.Filter, *auditlog.Position, int) ([]ports.AuditSummary, error) {
			return tc.rows, tc.err
		}))
		page, err := svc.List(context.Background(), timelineActor(), ports.AuditTimelineRequest{Limit: 1})
		if page != nil || !errors.Is(err, tc.want) {
			t.Fatalf("unsafe result: %+v %v", page, err)
		}
	}
}

func TestAuditTimelineCancellationWaitsForStorage(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := audit.NewTimelineService(timelineRead(func(context.Context, *ports.Actor, auditlog.Filter, *auditlog.Position, int) ([]ports.AuditSummary, error) {
		close(entered)
		<-release
		return []ports.AuditSummary{timelineRow(1)}, nil
	}))
	go func() { _, err := svc.List(ctx, timelineActor(), ports.AuditTimelineRequest{}); result <- err }()
	<-entered
	cancel()
	select {
	case err := <-result:
		close(release)
		t.Fatalf("returned before physical read: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("late result accepted: %v", err)
	}
}
