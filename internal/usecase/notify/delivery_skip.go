package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type DeliverySkipService struct{ repo ports.DeliverySkipRepository }

func NewDeliverySkipService(repo ports.DeliverySkipRepository) *DeliverySkipService {
	return &DeliverySkipService{repo}
}
func (s *DeliverySkipService) Skip(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.DeliverySkipCommand, key string) (*ports.DeliverySkipReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || !c.AcceptDeliveryLoss || c.ExpectedToken < 0 || c.ExpectedAttempts < 0 || c.ExpectedAttempts > 2147483647 || c.ExpectedUpdatedAt.IsZero() || key == "" || len(key) > 160 || (c.Reason != "handled_elsewhere" && c.Reason != "recipient_retired" && c.Reason != "event_obsolete") {
		return nil, ports.ErrInvalidDeliveryCommand
	}
	c.ExpectedUpdatedAt = c.ExpectedUpdatedAt.UTC()
	raw, _ := json.Marshal([]any{"integration.delivery.skip", id, c})
	hash := sha256.Sum256(raw)
	return s.repo.SkipIntegrationDelivery(ctx, a, id, c, key, hex.EncodeToString(hash[:]))
}
