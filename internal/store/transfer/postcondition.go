package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

// Reopen configured paths after physical I/O. A pinned descriptor alone cannot
// prove the directory still occupies that name when external writers rename it.
// This observation stays outside the transaction and while original slots are held.
func (s *Service) checkStepNamespace(ctx context.Context, plan db.TransferPlan, step db.TransferStep, marker string) error {
	target, err := s.files.OpenRoot(ctx, plan.RootPath.String)
	if err != nil {
		return err
	}
	defer target.Close()
	root, err := target.Identity(ctx)
	if err != nil {
		return err
	}
	if !plan.RootDevice.Valid || !plan.RootInode.Valid || root.Device != plan.RootDevice.Int64 || root.Inode != plan.RootInode.Int64 {
		return ErrReviewRequired
	}
	if _, err = target.Inspect(ctx, marker); err != nil {
		return err
	}
	identity, err := target.Inspect(ctx, step.TargetRel.String)
	if err != nil {
		return err
	}
	if identity != frozen(step) {
		return ErrReviewRequired
	}
	source, err := s.files.OpenRoot(ctx, step.SourceRoot.String)
	if err != nil {
		return err
	}
	defer source.Close()
	identity, err = source.Inspect(ctx, step.SourceRel.String)
	if err != nil {
		return err
	}
	if identity != frozen(step) {
		return ErrReviewRequired
	}
	return ctx.Err()
}
