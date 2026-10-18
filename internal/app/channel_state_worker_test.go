package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestChannelStateAppRunResumeAndInFlightOwnership(t *testing.T) {
	f := organizationSetup(t)
	_, _, dispatcher := testOutboxServices(f)
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Second)
	defer cancel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	var wrongSignature atomic.Bool
	var originalEvent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			calls.Add(1)
			raw, _ := io.ReadAll(r.Body)
			mac := hmac.New(sha256.New, []byte("private-hmac-secret"))
			mac.Write(raw)
			if r.Header.Get("X-PulseMedia-Event-ID") != originalEvent || r.Header.Get("X-PulseMedia-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
				wrongSignature.Store(true)
			}
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
				w.WriteHeader(http.StatusNoContent)
			case <-r.Context().Done():
			}
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	slow := createOutboxChannel(t, f, dispatcher, server.URL+"/slow")
	healthy := createOutboxChannel(t, f, dispatcher, server.URL+"/healthy")
	importForOutbox(t, f)
	svc := notify.NewChannelStateService(postgres.NewChannelStateStore(f.pool, idgen.New()))
	actor := notificationFixtureAdmin(t, f)
	row, e := f.q.GetNotificationChannelByID(ctx, slow)
	if e != nil {
		t.Fatal(e)
	}
	disabled, e := svc.Set(ctx, actor, slow, ports.ChannelStateCommand{ExpectedUpdatedAt: row.UpdatedAt.Time, IsEnabled: false}, "disable-slow-before-run")
	if e != nil {
		t.Fatal(e)
	}
	var deliveryID string
	if e = f.pool.QueryRow(ctx, "SELECT id::text,event_id::text FROM outbox_delivery WHERE consumer=$1", "notify:"+slow.String()).Scan(&deliveryID, &originalEvent); e != nil {
		t.Fatal(e)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, e := New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	waitOutboxCondition(t, ctx, func() bool {
		var delivered bool
		e := f.pool.QueryRow(ctx, "SELECT state='delivered' FROM outbox_delivery WHERE consumer=$1", "notify:"+healthy.String()).Scan(&delivered)
		return e == nil && delivered
	})
	var state string
	var attempts int
	if e = f.pool.QueryRow(ctx, "SELECT state,attempts FROM outbox_delivery WHERE id=$1", deliveryID).Scan(&state, &attempts); e != nil || state != "pending" || attempts != 0 || calls.Load() != 0 {
		t.Fatal("disabled did not preserve queue/budget", state, attempts, e)
	}
	if _, e = svc.Set(ctx, actor, slow, ports.ChannelStateCommand{ExpectedUpdatedAt: disabled.UpdatedAt, IsEnabled: true}, "enable-original-recipient"); e != nil {
		t.Fatal(e)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("enabled recipient never sent")
	}
	live, e := f.q.GetNotificationChannelByID(ctx, slow)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Set(ctx, actor, slow, ports.ChannelStateCommand{ExpectedUpdatedAt: live.UpdatedAt.Time, IsEnabled: false}, "disable-inflight"); e != nil {
		t.Fatal(e)
	}
	var owned bool
	if e = f.pool.QueryRow(ctx, "SELECT state='delivering' AND lease_owner IS NOT NULL AND lease_until>clock_timestamp() FROM outbox_delivery WHERE id=$1", deliveryID).Scan(&owned); e != nil || !owned {
		t.Fatal("logical disable released executing lease", e)
	}
	close(release)
	waitOutboxCondition(t, ctx, func() bool {
		var delivered bool
		e := f.pool.QueryRow(ctx, "SELECT state='delivered' FROM outbox_delivery WHERE id=$1", deliveryID).Scan(&delivered)
		return e == nil && delivered
	})
	if calls.Load() != 1 || wrongSignature.Load() {
		t.Fatal("wrong recipient/event/HMAC or duplicate", calls.Load(), wrongSignature.Load())
	}
	cancel()
	e = <-done
	joined = true
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
