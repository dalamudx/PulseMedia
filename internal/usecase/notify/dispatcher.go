package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var (
	ErrChannelNotFound = errors.New("notification channel not found")
)

type Dispatcher struct {
	reader   ports.NotificationChannelReader
	idgen    ports.IDGen
	clock    ports.Clock
	outbox   ports.NotificationDeliveryRepository
	clients  map[uuid.UUID]ports.Notifier
	mu       sync.RWMutex
	codec    ports.NotificationCredentialCodec
	factory  ClientFactory
	channels ports.NotificationChannelRepository
}

func NewDispatcher(reader ports.NotificationChannelReader, idgen ports.IDGen, clock ports.Clock, outbox ports.NotificationDeliveryRepository, channels ports.NotificationChannelRepository) *Dispatcher {
	return &Dispatcher{
		reader:   reader,
		channels: channels,
		idgen:    idgen,
		clock:    clock,
		outbox:   outbox,
		clients:  make(map[uuid.UUID]ports.Notifier),
	}
}

func (d *Dispatcher) RegisterClient(channelID uuid.UUID, client ports.Notifier) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clients[channelID] = client
}

// DispatchDelivery processes an Outbox notification delivery for a specific channel.
// Invariant: Failures in one channel do NOT block other channels (渠道独立重放).
// If an aggregate's delivery fails and moves to dead, only subsequent deliveries for that aggregate are blocked.
func (d *Dispatcher) DispatchDelivery(ctx context.Context, deliveryID, channelID uuid.UUID, owner string, leaseToken int64) error {
	delivery, event, err := d.outbox.LoadNotificationDelivery(ctx, deliveryID, "notify:"+channelID.String(), owner, leaseToken)
	if err != nil {
		return err
	}
	chRow, err := d.reader.LoadNotificationChannel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("channel lookup failed: %w", err)
	}

	if !chRow.IsEnabled {
		// Nothing has been sent. Pause without spending the recipient retry budget.
		return d.outbox.ReturnDisabledNotification(ctx, deliveryID, "notify:"+channelID.String(), owner, leaseToken)
	}

	d.mu.RLock()
	client := d.clients[channelID]
	factory := d.factory
	codec := d.codec
	d.mu.RUnlock()
	if client == nil {
		var cfg ChannelConfig
		if codec == nil {
			return ports.ErrUnavailable
		}
		plain, decodeErr := codec.DecryptSecret(chRow.ConfigEncrypted)
		if decodeErr != nil || json.Unmarshal(plain, &cfg) != nil || factory == nil || !validChannelConfig(chRow.ChannelType, cfg) {
			return ports.ErrUnavailable
		}
		client, err = factory(chRow.ChannelType, cfg)
		if err != nil || client == nil {
			return ports.ErrUnavailable
		}
	}

	notif := ports.Notification{
		EventID:   delivery.EventID.String(),
		Topic:     delivery.AggregateType,
		Title:     "PulseMedia: media update",
		Body:      fmt.Sprintf("%s (event %s)", notificationText(event.EventType), event.ID.String()),
		Severity:  "info",
		Timestamp: delivery.CreatedAt,
	}

	if event.EventType == "notification.test.v1" {
		notif.Title = "PulseMedia 测试通知"
		notif.Body = "用于检查通知通道连通性；此通知不改变业务数据。"
	}
	if event.EventType == "notification.media_imported.v1" {
		if err = applyBatchPresentation(&notif, event.AggregateID, event.Payload); err != nil {
			return err
		}
	}
	if _, _, err = d.outbox.LoadNotificationDelivery(ctx, deliveryID, "notify:"+channelID.String(), owner, leaseToken); err != nil {
		return err
	}
	current, err := d.reader.LoadNotificationChannel(ctx, channelID)
	if err != nil {
		return ports.ErrUnavailable
	}
	if !current.IsEnabled {
		return d.outbox.ReturnDisabledNotification(ctx, deliveryID, "notify:"+channelID.String(), owner, leaseToken)
	}
	sendErr := client.Send(ctx, notif)
	if sendErr != nil {
		retryAfter := 5 * time.Second
		if rle, ok := ports.IsRateLimited(sendErr); ok {
			retryAfter = rle.RetryAfter
		}
		// Adapter errors may embed webhook URLs, tokens or response bodies.
		// Persist and return only a stable, credential-free error class.
		if err := d.outbox.Fail(ctx, deliveryID, owner, leaseToken, "notification delivery failed", retryAfter); err != nil {
			return err
		}
		return errors.New("notification delivery failed")
	}

	return d.outbox.Finish(ctx, deliveryID, owner, leaseToken)
}
