package operation

import (
	"context"
	"errors"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type ActivityService struct{ reader ports.DashboardActivityReader }

func NewActivityService(reader ports.DashboardActivityReader) *ActivityService {
	return &ActivityService{reader: reader}
}

func (s *ActivityService) Read(ctx context.Context, actor *ports.Actor, limit int) (*ports.DashboardActivityPage, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.ID == "" ||
		(actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) ||
		(actor.Scopes != nil && !slices.Contains(actor.Scopes, "read")) {
		return nil, ports.ErrForbidden
	}
	if limit == 0 {
		limit = 6
	}
	if limit < 1 || limit > 12 {
		return nil, ports.ErrInvalidOperationQuery
	}
	if s == nil || s.reader == nil {
		return nil, ports.ErrUnavailable
	}
	page, err := s.reader.ReadDashboardActivity(ctx, actor, limit)
	if err != nil {
		if errors.Is(err, ports.ErrUnauthorized) || errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrInvalidOperationQuery) {
			return nil, err
		}
		return nil, ports.ErrUnavailable
	}
	if page == nil {
		return nil, ports.ErrUnavailable
	}
	return page, nil
}
