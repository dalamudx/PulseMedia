package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type RecoveryService struct {
	repo ports.TransferRecoveryRepository
}

func NewRecoveryService(repo ports.TransferRecoveryRepository) *RecoveryService {
	return &RecoveryService{repo}
}
func recoveryActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return ports.ErrForbidden
	}
	if _, e := uuid.Parse(a.ID); e != nil {
		return ports.ErrForbidden
	}
	return nil
}
func (s *RecoveryService) Preview(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.TransferRecoveryPreview, error) {
	if e := recoveryActor(a); e != nil {
		return nil, e
	}
	if id == uuid.Nil {
		return nil, ports.ErrTransferReviewRequired
	}
	return s.repo.PreviewOriginalTransferRecovery(ctx, a, id)
}
func (s *RecoveryService) Commit(ctx context.Context, a *ports.Actor, c ports.TransferRecoveryCommand, key string) (*ports.TransferRecoveryReceipt, error) {
	if e := recoveryActor(a); e != nil {
		return nil, e
	}
	if c.PlanID == uuid.Nil || !c.Confirmed || len(c.ExpectedInputStamp) != 64 || len(key) == 0 || len(key) > 140 || strings.TrimSpace(c.Reason) == "" || len([]rune(c.Reason)) > 240 || strings.ContainsAny(c.Reason, "\r\n") || media.UnsafeCandidateTitle(c.Reason) {
		return nil, ports.ErrTransferReviewRequired
	}
	for _, ch := range c.ExpectedInputStamp {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return nil, ports.ErrTransferReviewRequired
		}
	}
	return s.repo.CommitOriginalTransferRecovery(ctx, a, c, key)
}
