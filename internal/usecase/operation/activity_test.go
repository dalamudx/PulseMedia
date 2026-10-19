package operation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/operation"
)

type activityReader struct {
	calls int
	limit int
	actor *ports.Actor
	page  *ports.DashboardActivityPage
	err   error
}

func (r *activityReader) ReadDashboardActivity(_ context.Context, a *ports.Actor, limit int) (*ports.DashboardActivityPage, error) {
	r.calls++
	r.limit = limit
	r.actor = a
	return r.page, r.err
}
func TestActivityServiceBoundary(t *testing.T) {
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: "current", Role: ports.RoleMember}
	for _, tc := range []struct {
		name  string
		actor *ports.Actor
		limit int
		want  error
	}{
		{"missing", nil, 0, ports.ErrUnauthorized},
		{"service", &ports.Actor{Type: ports.ActorTypeService, ID: "current", Role: ports.RoleAdmin}, 0, ports.ErrForbidden},
		{"empty id", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleMember}, 0, ports.ErrForbidden},
		{"unknown role", &ports.Actor{Type: ports.ActorTypeUser, ID: "current", Role: "other"}, 0, ports.ErrForbidden},
		{"empty scopes", &ports.Actor{Type: ports.ActorTypeUser, ID: "current", Role: ports.RoleMember, Scopes: []string{}}, 0, ports.ErrForbidden},
		{"write scope", &ports.Actor{Type: ports.ActorTypeUser, ID: "current", Role: ports.RoleAdmin, Scopes: []string{"write"}}, 0, ports.ErrForbidden},
		{"negative", actor, -1, ports.ErrInvalidOperationQuery},
		{"too many", actor, 13, ports.ErrInvalidOperationQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &activityReader{}
			page, err := operation.NewActivityService(reader).Read(t.Context(), tc.actor, tc.limit)
			if page != nil || !errors.Is(err, tc.want) || reader.calls != 0 {
				t.Fatalf("page=%+v error=%v calls=%d", page, err, reader.calls)
			}
		})
	}
	for _, limit := range []int{0, 1, 12} {
		reader := &activityReader{page: &ports.DashboardActivityPage{Items: []ports.DashboardActivity{}}}
		page, err := operation.NewActivityService(reader).Read(t.Context(), actor, limit)
		want := limit
		if want == 0 {
			want = 6
		}
		if err != nil || page != reader.page || reader.limit != want || reader.actor != actor {
			t.Fatalf("read %d: %+v %v", limit, page, err)
		}
	}
	actor.Scopes = []string{"read"}
	for _, failure := range []error{ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrInvalidOperationQuery, errors.New("postgres://private:secret storage failure")} {
		reader := &activityReader{page: &ports.DashboardActivityPage{}, err: failure}
		page, err := operation.NewActivityService(reader).Read(t.Context(), actor, 6)
		want := failure
		if failure.Error() == "postgres://private:secret storage failure" {
			want = ports.ErrUnavailable
		}
		if page != nil || !errors.Is(err, want) {
			t.Fatalf("failure: %+v %v", page, err)
		}
	}
	for _, service := range []*operation.ActivityService{nil, operation.NewActivityService(nil), operation.NewActivityService(&activityReader{})} {
		if page, err := service.Read(t.Context(), actor, 6); page != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatalf("missing reader/result: %+v %v", page, err)
		}
	}
}
