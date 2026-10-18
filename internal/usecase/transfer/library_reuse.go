package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// LibraryReuseService keeps the local-demand origin independent of downloads.
// Persistence owns the narrow transaction; filesystem work owns root capabilities.
type LibraryReuseService struct{ repo ports.LibraryReuseRepository }

func NewLibraryReuseService(repo ports.LibraryReuseRepository) *LibraryReuseService {
	return &LibraryReuseService{repo: repo}
}
func (s *LibraryReuseService) Preview(ctx context.Context, a *ports.Actor, targets []ports.LibraryReuseTarget) ([]ports.LibraryReusePreview, error) {
	return s.repo.PreviewLibraryReuseBatch(ctx, a, targets)
}
func (s *LibraryReuseService) Confirm(ctx context.Context, a *ports.Actor, c ports.LibraryReuseCommand, key string) (*ports.LibraryReuseReceipt, error) {
	return s.repo.ConfirmLibraryReuse(ctx, a, c, key)
}
func (s *LibraryReuseService) List(ctx context.Context, a *ports.Actor) ([]ports.LibraryReuseStatus, error) {
	return s.repo.ListLibraryReuse(ctx, a)
}
func (s *LibraryReuseService) Execute(ctx context.Context, l ports.JobLease) error {
	return s.repo.ExecuteLibraryReuse(ctx, l)
}
