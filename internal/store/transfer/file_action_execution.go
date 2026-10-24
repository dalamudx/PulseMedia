package transfer

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func (s *Service) stepMediaEligible(step db.TransferStep) bool {
	source, _ := naming.IsEligibleMediaFile(step.SourceRel.String, step.SourceSize, s.minimumMediaBytes)
	target, _ := naming.IsEligibleMediaFile(step.TargetRel.String, step.SourceSize, s.minimumMediaBytes)
	return source && target
}

func (s *Service) executeHardlinkStep(ctx context.Context, roots map[string]ports.TransferRoot, plan db.TransferPlan, step db.TransferStep, marker string, authority []ports.JobLease) error {
	if err := s.startStep(ctx, plan, step, authority...); err != nil {
		return err
	}
	if s.stepMediaEligible(step) {
		if err := s.files.Hardlink(ctx, roots[step.SourceRoot.String], step.SourceRel.String, roots[plan.RootPath.String], step.TargetRel.String, frozen(step)); err != nil {
			return ErrReviewRequired
		}
	}
	if _, err := roots[plan.RootPath.String].Inspect(ctx, marker); err != nil {
		return ErrReviewRequired
	}
	observedAt, err := s.queries.GetSearchDatabaseTime(ctx)
	if err != nil {
		return err
	}
	if err = s.checkStepNamespace(ctx, plan, step, marker); err != nil {
		return ErrReviewRequired
	}
	return s.commitStep(ctx, plan, step, observedAt, authority...)
}

func (s *Service) executeVerifiedStep(ctx context.Context, roots map[string]ports.TransferRoot, plan db.TransferPlan, step db.TransferStep, marker string, authority []ports.JobLease) error {
	cap, ok := s.files.(ports.VerifiedTransferFilesystem)
	if !ok {
		return ErrReviewRequired
	}
	if err := s.startStep(ctx, plan, step, authority...); err != nil {
		return err
	}
	a, err := loadFileAction(ctx, s.queries, plan, step)
	if err != nil || a == nil {
		return ErrReviewRequired
	}
	source := roots[step.SourceRoot.String]
	sourceRoot, err := source.Identity(ctx)
	if err != nil || sourceRoot.Device != a.SourceRootDevice || sourceRoot.Inode != a.SourceRootInode {
		return ErrReviewRequired
	}
	if a.Content == nil {
		if !s.stepMediaEligible(step) {
			return ErrReviewRequired
		}
		proof, e := cap.InspectContent(ctx, source, step.SourceRel.String, frozen(step))
		if e != nil {
			return ErrReviewRequired
		}
		step, err = s.freezeFileActionContent(ctx, plan, step, proof, authority)
		if err != nil {
			return err
		}
		a, err = loadFileAction(ctx, s.queries, plan, step)
		if err != nil || a == nil || a.Content == nil {
			return ErrReviewRequired
		}
	}
	apply := cap.TransferVerified
	if !s.stepMediaEligible(step) {
		apply = cap.ObserveTransfer
	}
	result, err := apply(ctx, source, step.SourceRel.String, roots[plan.RootPath.String], step.TargetRel.String, *a.Content, step.Action, step.ID)
	if err != nil {
		if errors.Is(err, ports.ErrFileNoSpace) {
			return err
		}
		return ErrReviewRequired
	}
	observed, err := s.observeFileAction(ctx, plan, step, marker)
	if err != nil || observed.Target != result.Target || observed.SourceRemoved != result.SourceRemoved {
		return ErrReviewRequired
	}
	// Content verification can take longer than a web request or lease period.
	// Date the final observation after it completes, while physical slots are held.
	observedAt, err := s.queries.GetSearchDatabaseTime(ctx)
	if err != nil {
		return err
	}
	return s.commitObservedStep(ctx, plan, step, result.Target.File, observedAt, authority...)
}

func (s *Service) observeFileAction(ctx context.Context, plan db.TransferPlan, step db.TransferStep, marker string) (ports.VerifiedTransferResult, error) {
	var zero ports.VerifiedTransferResult
	cap, ok := s.files.(ports.VerifiedTransferFilesystem)
	if !ok {
		return zero, ErrReviewRequired
	}
	a, err := loadFileAction(ctx, s.queries, plan, step)
	if err != nil || a == nil || a.Content == nil {
		return zero, ErrReviewRequired
	}
	target, err := s.files.OpenRoot(ctx, plan.RootPath.String)
	if err != nil {
		return zero, err
	}
	defer target.Close()
	root, err := target.Identity(ctx)
	if err != nil {
		return zero, err
	}
	if root.Device != plan.RootDevice.Int64 || root.Inode != plan.RootInode.Int64 {
		return zero, ErrReviewRequired
	}
	if _, err = target.Inspect(ctx, marker); err != nil {
		return zero, err
	}
	source, err := s.files.OpenRoot(ctx, step.SourceRoot.String)
	if err != nil {
		return zero, err
	}
	defer source.Close()
	root, err = source.Identity(ctx)
	if err != nil {
		return zero, err
	}
	if root.Device != a.SourceRootDevice || root.Inode != a.SourceRootInode {
		return zero, ErrReviewRequired
	}
	return cap.ObserveTransfer(ctx, source, step.SourceRel.String, target, step.TargetRel.String, *a.Content, step.Action, step.ID)
}
