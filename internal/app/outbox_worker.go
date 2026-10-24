package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/im"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/telegram"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/webhook"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"sync"
	"time"
)

func notificationClientWithHTTP(kind string, c notify.ChannelConfig, h *http.Client, clock ports.Clock) (ports.Notifier, error) {
	switch kind {
	case "webhook":
		return webhook.NewClient(c.URL, c.Secret, h), nil
	case "telegram":
		return telegram.NewClient(c.BotToken, c.ChatID, h), nil
	case "wechat", "dingtalk", "feishu", "bark":
		return im.NewClient(kind, c.URL, c.Secret, h, clock, im.Artwork{URL: c.ImageURL, Key: c.ImageKey}), nil
	}
	return nil, ports.ErrUnavailable
}

func notificationClient(kind string, c notify.ChannelConfig) (ports.Notifier, error) {
	switch kind {
	case "webhook":
		return webhook.NewClient(c.URL, c.Secret, nil), nil
	case "telegram":
		return telegram.NewClient(c.BotToken, c.ChatID, nil), nil
	case "wechat", "dingtalk", "feishu", "bark":
		return im.NewClient(kind, c.URL, c.Secret, nil, nil, im.Artwork{URL: c.ImageURL, Key: c.ImageKey}), nil
	}
	return nil, ports.ErrUnavailable
}

// App owns every consumer, cancellation and join. Independent consumers prevent
// one slow/offline recipient from blocking other channels or media servers.
func (a *App) runOutboxWorkers(ctx context.Context, ob *outbox.Outbox, ms *mediaserver.SyncService, n *notify.Dispatcher) {
	q := db.New(a.dbPool)
	var wg sync.WaitGroup
	active := make(map[string]bool)
	slots := make(chan struct{}, 4)
	defer wg.Wait()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if _, err := ob.SealDueNotificationBatches(ctx, 16); err != nil && ctx.Err() == nil {
			a.logger.Error("notification batch sealing failed")
		}
		consumers, err := q.ListRuntimeOutboxConsumers(ctx)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("outbox consumer lookup failed")
		}
		for _, consumer := range consumers {
			if active[consumer] {
				continue
			}
			parts := strings.SplitN(consumer, ":", 2)
			if len(parts) != 2 {
				continue
			}
			recipient, err := uuid.Parse(parts[1])
			if err != nil {
				continue
			}
			active[consumer] = true
			wg.Add(1)
			go func(consumer, kind string, recipient uuid.UUID) {
				defer wg.Done()
				a.runOutboxConsumer(ctx, ob, ms, n, slots, consumer, kind, recipient)
			}(consumer, parts[0], recipient)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *App) runOutboxConsumer(ctx context.Context, ob *outbox.Outbox, ms *mediaserver.SyncService, n *notify.Dispatcher, slots chan struct{}, consumer, kind string, recipient uuid.UUID) {
	owner := a.idgen.NewUUID().String()
	q := db.New(a.dbPool)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		var permit *mediaserver.RefreshPermit
		if kind != "notify" && kind != "webpush" {
			var e error
			permit, e = ms.AcquireRefreshPermit(ctx, recipient)
			if e != nil {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					continue
				}
			}
		}
		if _, e := q.RecoverExpiredOutboxDeliveries(ctx, consumer); e != nil && ctx.Err() == nil {
			a.logger.Error("outbox recovery failed")
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			if permit != nil {
				permit.Release()
			}
			return
		}
		d, e := ob.Claim(ctx, consumer, owner, 2*time.Minute)
		if e != nil && ctx.Err() == nil {
			a.logger.Error("outbox claim failed")
		}
		if d != nil {
			work, cancel := context.WithTimeout(ctx, 35*time.Second)
			switch kind {
			case "webpush":
				e = a.processWebPushDelivery(work, ob, d.ID, recipient, owner, d.LeaseToken)
			case "notify":
				e = n.DispatchDelivery(work, d.ID, recipient, owner, d.LeaseToken)
			default:
				e = ms.ProcessReservedRefreshDelivery(work, permit, d.ID, recipient, owner, d.LeaseToken, "")
			}
			cancel()
			if e != nil && ctx.Err() == nil && !errors.Is(e, outbox.ErrStaleDeliveryLease) {
				current, read := q.GetOutboxDeliveryByID(ctx, d.ID)
				if read == nil && current.State == "delivering" && current.LeaseOwner.String == owner && current.LeaseToken == d.LeaseToken {
					if fail := ob.Fail(ctx, d.ID, owner, d.LeaseToken, "integration delivery failed", 10*time.Second); fail != nil {
						a.logger.Error("outbox failure write rejected")
					}
				}
			}
		}
		<-slots
		if permit != nil {
			permit.Release()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
