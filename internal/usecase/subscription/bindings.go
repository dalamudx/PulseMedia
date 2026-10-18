package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type BindingService struct {
	repo ports.SubscriptionBindingRepository
}

func NewBindingService(repo ports.SubscriptionBindingRepository) *BindingService {
	return &BindingService{repo}
}
func (s *BindingService) Configure(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.SubscriptionBindingsCommand, key string) (*ports.SubscriptionBindingsInfo, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || key == "" || len(key) > 160 || c.ExpectedVersion < 1 || c.IndexerIDs == nil || len(c.IndexerIDs) > 100 {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	if c.QualityPolicyID != nil && (*c.QualityPolicyID == "" || len(*c.QualityPolicyID) > 64) {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	seen := map[uuid.UUID]bool{}
	for _, v := range c.IndexerIDs {
		if v == uuid.Nil || seen[v] {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
		seen[v] = true
	}
	if c.DownloaderID != nil && *c.DownloaderID == uuid.Nil {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	raw, _ := json.Marshal(struct {
		ID      uuid.UUID
		Command ports.SubscriptionBindingsCommand
	}{id, c})
	sum := sha256.Sum256(raw)
	return s.repo.ConfigureBindings(ctx, a, id, c, key, hex.EncodeToString(sum[:]))
}
