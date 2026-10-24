package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"time"
)

type TestService struct {
	repo ports.NotificationTestRepository
}

func NewTestService(repo ports.NotificationTestRepository) *TestService { return &TestService{repo} }
func (s *TestService) Queue(ctx context.Context, a *ports.Actor, id uuid.UUID, expected time.Time, key string) (*ports.NotificationTestReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || expected.IsZero() || len(key) < 1 || len(key) > 160 {
		return nil, ErrInvalidConfig
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	raw, _ := json.Marshal([]any{"notification.test.v1", id, expected.UTC()})
	sum := sha256.Sum256(raw)
	return s.repo.QueueNotificationTest(ctx, a, id, expected.UTC(), key, hex.EncodeToString(sum[:]))
}
