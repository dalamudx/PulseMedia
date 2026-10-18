package operation

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func (s *Service) List(ctx context.Context, a *ports.Actor, request ports.OperationListRequest) (*ports.OperationPage, error) {
	if a == nil || a.ID == "" {
		return nil, ports.ErrForbidden
	}
	if request.View == "" {
		request.View = "active"
	}
	if request.Limit == 0 {
		request.Limit = 25
	}
	if (request.View != "active" && request.View != "review" && request.View != "all") || request.Limit < 1 || request.Limit > 50 || len(request.Cursor) > 512 {
		return nil, ports.ErrInvalidOperationQuery
	}
	if s == nil || s.store == nil {
		return nil, ports.ErrUnavailable
	}
	return s.store.ListOperations(ctx, a, request)
}

type Service struct{ store ports.OperationQuery }

func NewService(store ports.OperationQuery) *Service { return &Service{store: store} }
func (s *Service) Read(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.OperationSnapshot, error) {
	if a == nil || a.ID == "" || id == uuid.Nil {
		return nil, ports.ErrForbidden
	}
	if s == nil || s.store == nil {
		return nil, ports.ErrUnavailable
	}
	return s.store.ReadOperation(ctx, a, id)
}
