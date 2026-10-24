package notify

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/webpush"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type WebPushDeliveryService struct {
	repo     ports.WebPushDeliveryRepository
	decoder  ports.CredentialDecoder
	failures ports.DeliveryFailureWriter
	sender   ports.WebPushSender
	version  string
}

func NewWebPushDeliveryService(repo ports.WebPushDeliveryRepository, decoder ports.CredentialDecoder, failures ports.DeliveryFailureWriter, sender ports.WebPushSender, version string) *WebPushDeliveryService {
	return &WebPushDeliveryService{repo, decoder, failures, sender, version}
}
func (s *WebPushDeliveryService) Dispatch(ctx context.Context, id, device uuid.UUID, owner string, token int64) (err error) {
	if s == nil || s.repo == nil || s.decoder == nil || s.failures == nil {
		return ports.ErrUnavailable
	}
	if id == uuid.Nil || device == uuid.Nil || owner == "" || token <= 0 {
		return ports.ErrStaleDeliveryLease
	}
	execution, err := s.repo.OpenWebPushExecution(ctx, device)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := execution.Close()
		if err == nil {
			err = closeErr
		}
	}()
	owned, err := execution.Load(ctx, id, owner, token, s.version)
	if errors.Is(err, ports.ErrForbidden) {
		return execution.Complete(ctx, id, owner, token, "skipped")
	}
	if err != nil {
		return err
	}
	plain, err := s.decoder.DecryptSecret(owned.Ciphertext)
	if err != nil {
		return ports.ErrUnavailable
	}
	var subscription ports.WebPushSubscription
	if json.Unmarshal(plain, &subscription) != nil || !webpush.ValidSubscription(subscription) {
		return ports.ErrUnavailable
	}
	// Recheck after decryption; no transaction remains open during network I/O.
	current, err := execution.Load(ctx, id, owner, token, s.version)
	if errors.Is(err, ports.ErrForbidden) {
		return execution.Complete(ctx, id, owner, token, "skipped")
	}
	if err != nil {
		return err
	}
	if current.EventID != owned.EventID {
		return ports.ErrStaleDeliveryLease
	}
	if s.sender == nil {
		return ports.ErrUnavailable
	}
	payload := ports.WebPushPayload{Title: "媒体已入库", Body: "关注的媒体已有新文件入库，打开 PulseMedia 查看。", URL: "/", Tag: "pulsemedia:" + owned.EventID.String()}
	sendErr := s.sender.SendNotification(ctx, subscription, payload)
	if sendErr == nil {
		return execution.Complete(ctx, id, owner, token, "accepted")
	}
	if errors.Is(sendErr, ports.ErrNotFound) {
		return execution.Complete(ctx, id, owner, token, "expired")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	delay := 10 * time.Second
	if rate, ok := ports.IsRateLimited(sendErr); ok {
		delay = rate.RetryAfter
		if delay <= 0 {
			delay = 30 * time.Second
		}
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return s.failures.Fail(ctx, id, owner, token, "browser push service unavailable", delay)
}
