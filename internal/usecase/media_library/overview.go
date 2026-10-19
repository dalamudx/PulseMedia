package media_library

import (
	"context"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type OverviewService struct{ reader ports.LibraryOverviewReader }

func NewOverviewService(reader ports.LibraryOverviewReader) *OverviewService {
	return &OverviewService{reader: reader}
}

func (s *OverviewService) Read(ctx context.Context, actor *ports.Actor, in ports.LibraryOverviewRequest) (*ports.LibraryOverview, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || (actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) || (actor.Scopes != nil && !slices.Contains(actor.Scopes, "read")) {
		return nil, ports.ErrForbidden
	}
	if in.RecentLimit == 0 {
		in.RecentLimit = 12
	}
	if in.RecentLimit < 1 || in.RecentLimit > 24 {
		return nil, ports.ErrInvalidLibraryQuery
	}
	if s == nil || s.reader == nil {
		return nil, ports.ErrUnavailable
	}
	return s.reader.ReadLibraryOverview(ctx, actor, in)
}
