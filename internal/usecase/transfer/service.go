package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var ErrSymlinkForbidden = ports.ErrFileSymlink
var ErrInodeMismatch = ports.ErrFileIdentity
var ErrReviewRequired = ports.ErrTransferReviewRequired
var ErrExecutionRejected = ports.ErrTransferExecutionRejected

type Service struct{ repo ports.TransferRepository }

func NewService(repo ports.TransferRepository) *Service { return &Service{repo: repo} }
func (s *Service) CreateMoviePlan(ctx context.Context, op, root, unit uuid.UUID, path, title string, year int, profile quality.Profile) (*ports.TransferPlan, *ports.TransferStep, error) {
	p, e := s.repo.PrepareMovieTransfer(ctx, ports.MovieTransferRequest{OperationID: op, RootID: root, UnitID: unit, SourcePath: path, Title: title, Year: year, Quality: profile})
	if e != nil {
		return nil, nil, e
	}
	return &p.Plan, &p.Step, nil
}
func (s *Service) ExecutePlan(ctx context.Context, id uuid.UUID) error {
	return s.repo.ExecuteTransfer(ctx, id)
}
func (s *Service) OrganizeMovie(ctx context.Context, op, root, unit uuid.UUID, path, title string, year int, profile quality.Profile) error {
	p, _, e := s.CreateMoviePlan(ctx, op, root, unit, path, title, year, profile)
	if e != nil {
		return e
	}
	return s.ExecutePlan(ctx, p.ID)
}
func (s *Service) UpgradeMediaFile(ctx context.Context, op, root, unit, old uuid.UUID, path, title string, year int, profile quality.Profile) error {
	p, e := s.repo.PrepareMovieTransfer(ctx, ports.MovieTransferRequest{OperationID: op, RootID: root, UnitID: unit, OldFileID: &old, SourcePath: path, Title: title, Year: year, Quality: profile})
	if e != nil {
		return e
	}
	return s.ExecutePlan(ctx, p.Plan.ID)
}
func (s *Service) OrganizeDownload(ctx context.Context, lease ports.JobLease) error {
	return s.repo.OrganizeCompletedDownload(ctx, lease)
}
