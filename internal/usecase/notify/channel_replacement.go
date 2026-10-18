package notify

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type ChannelReplacementService struct {
	repo    ports.ChannelReplacementRepository
	secrets ports.SecretProtector
}

func NewChannelReplacementService(repo ports.ChannelReplacementRepository, secrets ports.SecretProtector) *ChannelReplacementService {
	return &ChannelReplacementService{repo, secrets}
}
func (s *ChannelReplacementService) Replace(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.ChannelReplacementCommand, key string) (*ports.ChannelReplacementReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	c.Name = strings.TrimSpace(c.Name)
	c.ExpectedUpdatedAt = c.ExpectedUpdatedAt.UTC()
	if id == uuid.Nil || c.ExpectedUpdatedAt.IsZero() || !c.AcceptOldPending || c.Name == "" || len([]rune(c.Name)) > 120 || key == "" || len(key) > 160 || !validChannelConfig(c.ChannelType, c.Config) || len(c.Config.URL) > 4000 || len(c.Config.Secret) > 1000 || len(c.Config.BotToken) > 1000 || len(c.Config.ChatID) > 1000 {
		return nil, ports.ErrInvalidChannelReplacement
	}
	raw, _ := json.Marshal([]any{"notification.channel.replace.v1", id, c})
	digest, e := s.secrets.DigestSecret(raw)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	plain, _ := json.Marshal(c.Config)
	cipher, e := s.secrets.EncryptSecret(plain)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	return s.repo.ReplaceNotificationChannel(ctx, a, id, ports.ChannelReplacementWrite{ExpectedUpdatedAt: c.ExpectedUpdatedAt, Name: c.Name, ChannelType: c.ChannelType, Ciphertext: cipher}, key, digest)
}
