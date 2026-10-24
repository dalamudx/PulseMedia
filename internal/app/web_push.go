package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	nativepush "github.com/dalamudx/PulseMedia/internal/adapter/notifier/webpush"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

func (a *App) webPushVersion() string {
	if !a.cfg.WebPush.Enabled {
		return ""
	}
	sum := sha256.Sum256([]byte(a.cfg.WebPush.PublicKey))
	return hex.EncodeToString(sum[:])
}
func (a *App) enableWebPush(ob *outbox.Outbox) {
	if a.cfg.WebPush.Enabled {
		ob.EnableWebPush(a.webPushVersion())
	}
}
func (a *App) processWebPushDelivery(ctx context.Context, ob *outbox.Outbox, id, device uuid.UUID, owner string, token int64) error {
	var sender ports.WebPushSender
	if a.cfg.WebPush.Enabled {
		client, err := nativepush.NewClient(nativepush.Config{PublicKey: a.cfg.WebPush.PublicKey, PrivateKey: a.cfg.WebPush.PrivateKey, Subject: a.cfg.WebPush.Subject}, a.notificationHTTPClient, a.clock.Now)
		if err != nil {
			return ports.ErrUnavailable
		}
		sender = client
	}
	service := notify.NewWebPushDeliveryService(postgres.NewWebPushDeliveryStore(a.dbPool), a.indexerDecoder, ob, sender, a.webPushVersion())
	return service.Dispatch(ctx, id, device, owner, token)
}
