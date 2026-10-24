package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.CompleteTVOrganizationConfirmationRepository = (*Repository)(nil)

// Resolve only a persisted tracking reference, after the same current account and
// subscription locks that protect cached receipts. The existing confirmation
// transaction still revalidates every material and freezes the entire plan.
func (r *Repository) ConfirmCompleteTVOrganizationReferences(ctx context.Context, actor *ports.Actor, refs ports.CompleteTVOrganizationReferences, key string) (out *ports.CompleteTVOrganizationReceipt, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil && !errors.Is(err, ports.ErrTransferReviewRequired) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrForbidden) {
			err = ports.ErrUnavailable
		}
	}()
	c := ports.CompleteTVOrganizationCommand{TaskID: refs.TaskID, OperationID: refs.OperationID, TrackingJobID: refs.TrackingJobID, Confirmed: true, NamingVersion: refs.NamingVersion}
	tx, err := r.execution.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := r.execution.queries.WithTx(tx)
	if _, err = completeTVPermission(ctx, q, actor, c); err != nil {
		return nil, err
	}
	job, err := q.GetJobByID(ctx, refs.TrackingJobID)
	if err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	var receipt ports.CompleteTVTrackingReceipt
	if json.Unmarshal(job.Result, &receipt) != nil || receipt.TaskID != refs.TaskID || len(receipt.EvidenceStamp) != 64 {
		return nil, ports.ErrTransferReviewRequired
	}
	c.ExpectedEvidenceStamp = receipt.EvidenceStamp
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.ConfirmCompleteTVOrganization(ctx, actor, c, key)
}
