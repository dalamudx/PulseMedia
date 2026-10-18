package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type ChannelStateService struct{ repo ports.ChannelStateRepository }

func NewChannelStateService(repo ports.ChannelStateRepository) *ChannelStateService {
	return &ChannelStateService{repo}
}
func (s *ChannelStateService) Set(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.ChannelStateCommand, key string) (*ports.ChannelStateReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || c.ExpectedUpdatedAt.IsZero() || key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidChannelState
	}
	c.ExpectedUpdatedAt = c.ExpectedUpdatedAt.UTC()
	raw, _ := json.Marshal([]any{"notification.channel.state", id, c})
	h := sha256.Sum256(raw)
	return s.repo.SetChannelState(ctx, a, id, c, key, hex.EncodeToString(h[:]))
}
