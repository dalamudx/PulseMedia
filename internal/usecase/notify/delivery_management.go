package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type DeliveryManagementService struct {
	repo ports.DeliveryManagementRepository
}

func NewDeliveryManagementService(repo ports.DeliveryManagementRepository) *DeliveryManagementService {
	return &DeliveryManagementService{repo}
}
func (s *DeliveryManagementService) List(ctx context.Context, a *ports.Actor, query ports.DeliveryListQuery) (*ports.DeliveryListPage, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	return s.repo.ListIntegrationDeliveries(ctx, a, query)
}
func (s *DeliveryManagementService) Replay(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.DeliveryReplayCommand, key string) (*ports.DeliveryReplayReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || !c.AcceptDuplicateRisk || c.ExpectedToken < 0 || c.ExpectedAttempts < 1 || c.ExpectedUpdatedAt.IsZero() || key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidDeliveryCommand
	}
	raw, _ := json.Marshal([]any{"integration.delivery.replay", id, c})
	h := sha256.Sum256(raw)
	return s.repo.ReplayIntegrationDelivery(ctx, a, id, c, key, hex.EncodeToString(h[:]))
}
