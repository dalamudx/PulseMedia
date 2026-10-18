package search

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var _ ports.InteractiveSearchExecutor = (*Service)(nil)

// ExecuteInteractiveSearchSource returns only after all physical calls return.
// A deadline stops new pages; it never releases an endpoint permit early.
func (s *Service) ExecuteInteractiveSearchSource(ctx context.Context, lease ports.InteractiveSearchSourceLease) error {
	if lease.Task.SearchID == uuid.Nil || lease.Task.IndexerID == uuid.Nil || lease.Task.ConfigVersion < 1 || lease.Task.Lease.ID == uuid.Nil || lease.Task.Lease.Owner == "" || lease.Task.Lease.Token < 1 || lease.ActorID == uuid.Nil || lease.Query == "" || lease.Deadline.IsZero() {
		return ports.ErrBadResponse
	}
	bounded, cancel := context.WithDeadline(ctx, lease.Deadline)
	defer cancel()
	ids, executionErr := s.aggregateEndpointPagesResult(bounded, lease.Task.IndexerID, lease.Task.ConfigVersion, lease.Query, &lease)
	if err := bounded.Err(); err != nil {
		return err
	}
	if executionErr != nil {
		return executionErr
	}
	if len(ids) == 0 {
		return ports.ErrUnavailable
	}
	return nil
}
