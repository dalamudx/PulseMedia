package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/webhook"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotificationDisabledDuringFactoryNeverSends(t *testing.T) {
	f := organizationSetup(t)
	ob, _, n := testOutboxServices(f)
	ctx := context.Background()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	channel := createOutboxChannel(t, f, n, server.URL)
	importForOutbox(t, f)
	consumer := "notify:" + channel.String()
	d, e := ob.Claim(ctx, consumer, "owner", time.Minute)
	if e != nil || d == nil {
		t.Fatal(e)
	}
	svc := notify.NewChannelStateService(postgres.NewChannelStateStore(f.pool, idgen.New()))
	var factoryErr error
	n.SetClientFactory(crypto.NewSecretProtector(outboxTestKey), func(kind string, c notify.ChannelConfig) (ports.Notifier, error) {
		row, e := f.q.GetNotificationChannelByID(ctx, channel)
		if e != nil {
			return nil, e
		}
		_, factoryErr = svc.Set(ctx, notificationFixtureAdmin(t, f), channel, ports.ChannelStateCommand{ExpectedUpdatedAt: row.UpdatedAt.Time, IsEnabled: false}, "disable-during-client-construction")
		if factoryErr != nil {
			return nil, factoryErr
		}
		return webhook.NewClient(c.URL, c.Secret, nil), nil
	})
	if e = n.DispatchDelivery(ctx, d.ID, channel, "owner", d.LeaseToken); e != nil || factoryErr != nil {
		t.Fatal(e, factoryErr)
	}
	after, e := f.q.GetOutboxDeliveryByID(ctx, d.ID)
	if e != nil || after.State != "pending" || after.Attempts != 0 || after.EventID != d.EventID || after.LeaseOwner.Valid || requests.Load() != 0 {
		t.Fatal("configuration race sent or consumed budget", after, e, requests.Load())
	}
}
