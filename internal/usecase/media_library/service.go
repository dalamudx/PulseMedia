package media_library

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type Service struct{ reader ports.LibraryCatalogReader }

func NewService(r ports.LibraryCatalogReader) *Service { return &Service{reader: r} }
func (s *Service) List(ctx context.Context, a *ports.Actor, r ports.LibraryCatalogRequest) (*ports.LibraryCatalogPage, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin && a.Role != ports.RoleMember {
		return nil, ports.ErrForbidden
	}
	if a.Scopes != nil {
		allowed := false
		for _, scope := range a.Scopes {
			if scope == "read" {
				allowed = true
			}
		}
		if !allowed {
			return nil, ports.ErrForbidden
		}
	}
	if r.Limit == 0 {
		r.Limit = 50
	}
	if r.Limit < 1 || r.Limit > 100 || len(r.Cursor) > 1024 {
		return nil, ports.ErrInvalidLibraryQuery
	}
	return s.reader.ListLibraryCatalog(ctx, a, r)
}
