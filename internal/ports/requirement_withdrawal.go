package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrRequirementConflict = errors.New("requirement command or source changed")

type RequirementInfo struct {
	ID          uuid.UUID `json:"id"`
	OperationID uuid.UUID `json:"operation_id"`
	UnitID      uuid.UUID `json:"unit_id"`
	RootID      uuid.UUID `json:"root_id"`
	State       string    `json:"state"`
	FenceDigest string    `json:"fence_digest"`
}
type RequirementWithdrawalCommand struct {
	OperationID, RequirementID uuid.UUID
	ExpectedFence              string
	Key, Hash                  string
}
type RequirementWithdrawalReceipt struct {
	OperationID      uuid.UUID `json:"operation_id"`
	RequirementID    uuid.UUID `json:"requirement_id"`
	State            string    `json:"state"`
	PhysicalReleased bool      `json:"physical_released"`
}
type RequirementWithdrawalRepository interface {
	ListOwnRequirements(context.Context, *Actor, uuid.UUID) ([]RequirementInfo, error)
	WithdrawRequirement(context.Context, *Actor, RequirementWithdrawalCommand) (*RequirementWithdrawalReceipt, error)
}
