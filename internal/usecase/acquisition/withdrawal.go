package acquisition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type WithdrawalService struct {
	repo ports.RequirementWithdrawalRepository
}

func NewWithdrawalService(repo ports.RequirementWithdrawalRepository) *WithdrawalService {
	return &WithdrawalService{repo}
}
func (s *WithdrawalService) List(ctx context.Context, a *ports.Actor, id uuid.UUID) ([]ports.RequirementInfo, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	return s.repo.ListOwnRequirements(ctx, a, id)
}
func (s *WithdrawalService) Withdraw(ctx context.Context, a *ports.Actor, c ports.RequirementWithdrawalCommand) (*ports.RequirementWithdrawalReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if c.OperationID == uuid.Nil || c.RequirementID == uuid.Nil || len(c.ExpectedFence) != 64 || len(c.Key) < 1 || len(c.Key) > 160 {
		return nil, ports.ErrRequirementConflict
	}
	if _, e := hex.DecodeString(c.ExpectedFence); e != nil {
		return nil, ports.ErrRequirementConflict
	}
	raw, _ := json.Marshal([]any{"requirement.withdraw", c.OperationID, c.RequirementID, c.ExpectedFence})
	sum := sha256.Sum256(raw)
	c.Hash = hex.EncodeToString(sum[:])
	return s.repo.WithdrawRequirement(ctx, a, c)
}
