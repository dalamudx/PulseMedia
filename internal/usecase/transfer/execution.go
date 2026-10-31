package transfer

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// ExecutePreparedPlan coordinates frozen steps while storage retains all physical
// identity, transaction and lease checks. Release never races a running syscall.
func ExecutePreparedPlan(ctx context.Context, repo ports.TransferExecutionPreparer, id uuid.UUID, authority ...ports.JobLease) error {
	attempt, err := repo.PrepareTransferExecution(ctx, id, authority...)
	if attempt != nil && attempt.Release != nil {
		defer attempt.Release()
	}
	if err != nil {
		return err
	}
	if attempt == nil || attempt.Complete == nil {
		return ports.ErrUnavailable
	}
	if attempt.Completed {
		return attempt.Complete(ctx)
	}
	if attempt.Release == nil || attempt.Review == nil {
		return ports.ErrUnavailable
	}
	if attempt.ReviewRequired {
		return attempt.Review(ctx)
	}
	if len(attempt.Steps) == 0 {
		return attempt.Review(ctx)
	}
	for _, step := range attempt.Steps {
		if step.State == "completed" {
			continue
		}
		if step.State == "pending" && !step.Eligible {
			return attempt.Review(ctx)
		}
		if step.Execute == nil {
			return ports.ErrUnavailable
		}
		if err := step.Execute(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ports.ErrTransferReviewRequired) {
				return attempt.Review(ctx)
			}
			return err
		}
	}
	return attempt.Complete(ctx)
}
