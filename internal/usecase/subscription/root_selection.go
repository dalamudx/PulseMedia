package subscription

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type RootSelectionService struct {
	repo ports.LibraryRootSelectionRepository
}

func NewRootSelectionService(repo ports.LibraryRootSelectionRepository) *RootSelectionService {
	return &RootSelectionService{repo}
}
func (s *RootSelectionService) List(ctx context.Context, a *ports.Actor) ([]ports.LibraryRootSelection, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	return s.repo.ListLibraryRootSelections(ctx)
}
