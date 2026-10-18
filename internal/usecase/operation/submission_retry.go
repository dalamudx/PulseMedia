package operation

import (
	"context"
	"encoding/hex"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type SubmissionRetryService struct {
	repo ports.SubmissionRetryRepository
}

func NewSubmissionRetryService(repo ports.SubmissionRetryRepository) *SubmissionRetryService {
	return &SubmissionRetryService{repo: repo}
}
func retryActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) {
		return ports.ErrForbidden
	}
	return nil
}
func (s *SubmissionRetryService) Preview(ctx context.Context, a *ports.Actor, op uuid.UUID) (*ports.SubmissionRetryPreview, error) {
	if e := retryActor(a); e != nil {
		return nil, e
	}
	if op == uuid.Nil {
		return nil, ports.ErrSubmissionRetryConflict
	}
	return s.repo.PreviewSubmissionRetry(ctx, a, op)
}
func (s *SubmissionRetryService) Retry(ctx context.Context, a *ports.Actor, c ports.SubmissionRetryCommand) (*ports.SubmissionRetryReceipt, error) {
	if e := retryActor(a); e != nil {
		return nil, e
	}
	_, e := hex.DecodeString(c.ExpectedStamp)
	if c.OperationID == uuid.Nil || c.JobID == uuid.Nil || !c.Acknowledged || len(c.Key) == 0 || len(c.Key) > 200 || strings.TrimSpace(c.Key) != c.Key || len(c.ExpectedStamp) != 64 || e != nil {
		return nil, ports.ErrSubmissionRetryConflict
	}
	return s.repo.RetrySubmission(ctx, a, c)
}
