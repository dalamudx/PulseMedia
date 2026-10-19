package operation

import (
	"context"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type TimelineService struct{ reader ports.OperationTimelineReader }

func NewTimelineService(reader ports.OperationTimelineReader) *TimelineService {
	return &TimelineService{reader: reader}
}

func (s *TimelineService) Read(ctx context.Context, actor *ports.Actor, id uuid.UUID, request ports.OperationTimelineRequest) (*ports.OperationTimelinePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if actor == nil || actor.Type != ports.ActorTypeUser || actor.ID == "" ||
		(actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) ||
		(actor.Scopes != nil && !slices.Contains(actor.Scopes, "read")) {
		return nil, ports.ErrForbidden
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	if id == uuid.Nil || request.Limit < 1 || request.Limit > 100 || len(request.Cursor) > 1024 {
		return nil, ports.ErrInvalidOperationQuery
	}
	if s == nil || s.reader == nil {
		return nil, ports.ErrUnavailable
	}
	page, err := s.reader.ReadOperationTimeline(ctx, actor, id, request)
	if err == nil && page == nil {
		return nil, ports.ErrUnavailable
	}
	return page, err
}
