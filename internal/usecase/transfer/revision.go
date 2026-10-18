package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type RevisionService struct {
	repo ports.TransferRevisionRepository
}

func NewRevisionService(repo ports.TransferRevisionRepository) *RevisionService {
	return &RevisionService{repo}
}
func ValidRevisionPath(target string) bool {
	return naming.ValidManualTarget(target) && !media.UnsafeCandidateTitle(target)
}
func (s *RevisionService) Preview(ctx context.Context, a *ports.Actor, id uuid.UUID, target string) (*ports.TransferRevisionPreview, error) {
	if e := recoveryActor(a); e != nil {
		return nil, e
	}
	if id == uuid.Nil || !ValidRevisionPath(target) {
		return nil, ports.ErrTransferReviewRequired
	}
	return s.repo.PreviewTransferRevision(ctx, a, id, target)
}
func (s *RevisionService) PreviewAction(ctx context.Context, a *ports.Actor, id uuid.UUID, target, action string) (*ports.TransferRevisionPreview, error) {
	if action == "" || action == "hardlink" {
		return s.Preview(ctx, a, id, target)
	}
	if err := recoveryActor(a); err != nil {
		return nil, err
	}
	if id == uuid.Nil || !ValidRevisionPath(target) || !validRevisionAction(action) {
		return nil, ports.ErrTransferReviewRequired
	}
	repo, ok := s.repo.(ports.TransferActionRevisionRepository)
	if !ok {
		return nil, ports.ErrTransferReviewRequired
	}
	return repo.PreviewTransferActionRevision(ctx, a, id, target, action)
}
func validRevisionAction(action string) bool {
	return action == "" || action == "hardlink" || action == "copy" || action == "reflink" || action == "move"
}
func (s *RevisionService) Commit(ctx context.Context, a *ports.Actor, c ports.TransferRevisionCommand, key string) (*ports.TransferRevisionReceipt, error) {
	if e := recoveryActor(a); e != nil {
		return nil, e
	}
	if c.PlanID == uuid.Nil || !ValidRevisionPath(c.TargetRel) || !validRevisionAction(c.Action) || !c.Confirmed || len(c.ExpectedInputStamp) != 64 || key == "" || len(key) > 140 || strings.TrimSpace(c.Reason) == "" || len([]rune(c.Reason)) > 240 || strings.ContainsAny(c.Reason, "\r\n") || media.UnsafeCandidateTitle(c.Reason) {
		return nil, ports.ErrTransferReviewRequired
	}
	for _, v := range c.ExpectedInputStamp {
		if (v < '0' || v > '9') && (v < 'a' || v > 'f') {
			return nil, ports.ErrTransferReviewRequired
		}
	}
	return s.repo.CommitTransferRevision(ctx, a, c, key)
}
