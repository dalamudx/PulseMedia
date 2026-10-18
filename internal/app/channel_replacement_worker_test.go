package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestChannelReplacementAppRunKeepsOldPhysicalLeaseAndOnlyFutureRecipient(t *testing.T) {
	f := organizationSetup(t)
	ob, _, n := testOutboxServices(f)
	started := make(chan string, 2)
	release := make(chan struct{})
	newReceipt := make(chan string, 2)
	bad := make(chan string, 4)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		secret := "private-hmac-secret"
		if r.URL.Path == "/new" {
			secret = "private-new-hmac"
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		if r.Header.Get("X-PulseMedia-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			bad <- "wrong original/replacement HMAC"
		}
		switch r.URL.Path {
		case "/old":
			started <- r.Header.Get("X-PulseMedia-Event-ID")
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		case "/new":
			newReceipt <- r.Header.Get("X-PulseMedia-Event-ID")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	oldID := createOutboxChannel(t, f, n, server.URL+"/old")
	importForOutbox(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	pendingEvent, e := ob.Publish(ctx, f.q.WithTx(tx), "fixture.pending", "old-only", "notification.fixture", map[string]string{"fixture": "unconfirmed history"}, []string{"notify:" + oldID.String()})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
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
		releaseOnce.Do(func() { close(release) })
		cancel()
		if !joined {
			<-done
		}
	}()
	var originalID string
	select {
	case originalID = <-started:
	case <-ctx.Done():
		t.Fatal("old send never started")
	}
	var deliveryID uuid.UUID
	if e = f.pool.QueryRow(ctx, "SELECT id FROM outbox_delivery WHERE event_id=$1::uuid AND consumer=$2", originalID, "notify:"+oldID.String()).Scan(&deliveryID); e != nil {
		t.Fatal(e)
	}
	before, e := f.q.GetOutboxDeliveryByID(ctx, deliveryID)
	if e != nil || before.State != "delivering" || !before.LeaseOwner.Valid {
		t.Fatal("missing physical authority", e)
	}
	old, e := f.q.GetNotificationChannelByID(ctx, oldID)
	if e != nil {
		t.Fatal(e)
	}
	replacement := notify.NewChannelReplacementService(postgres.NewChannelReplacementStore(f.pool, f.gen), crypto.NewSecretProtector(outboxTestKey))
	r, e := replacement.Replace(ctx, notificationFixtureAdmin(t, f), oldID, ports.ChannelReplacementCommand{ExpectedUpdatedAt: old.UpdatedAt.Time, Name: "Replacement Hook", ChannelType: "webhook", Config: ports.NotificationChannelConfig{URL: server.URL + "/new", Secret: "private-new-hmac"}, AcceptOldPending: true}, "physical-replacement")
	if e != nil {
		t.Fatal(e)
	}
	after, e := f.q.GetOutboxDeliveryByID(ctx, deliveryID)
	if e != nil || after.State != "delivering" || after.LeaseToken != before.LeaseToken || after.LeaseOwner.String != before.LeaseOwner.String || !after.LeaseUntil.Time.Equal(before.LeaseUntil.Time) {
		t.Fatal("replacement canceled physical old send", e)
	}
	var fileID uuid.UUID
	if e = f.pool.QueryRow(ctx, "SELECT id FROM library_file WHERE root_id=$1 AND retired_at IS NULL", f.root).Scan(&fileID); e != nil {
		t.Fatal(e)
	}
	tx, e = f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ob.PublishImported(ctx, f.q.WithTx(tx), f.unit.String(), map[string]any{"file_id": fileID, "unit_id": f.unit}); e != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var newID string
	select {
	case newID = <-newReceipt:
	case <-ctx.Done():
		t.Fatal("new recipient starved by old in-flight network")
	}
	waitOutboxCondition(t, ctx, func() bool {
		var ready bool
		e := f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM outbox_delivery WHERE event_id=$1::uuid AND consumer=$2 AND state='delivered')", newID, "notify:"+r.NewChannelID.String()).Scan(&ready)
		return e == nil && ready
	})
	after, e = f.q.GetOutboxDeliveryByID(ctx, deliveryID)
	if e != nil || after.State != "delivering" {
		t.Fatal("old result fabricated before HTTP completion", e)
	}
	var copied int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM outbox_delivery WHERE event_id=$1 AND consumer=$2", pendingEvent.ID, "notify:"+r.NewChannelID.String()).Scan(&copied); e != nil || copied != 0 {
		t.Fatal("old pending retargeted", e)
	}
	releaseOnce.Do(func() { close(release) })
	waitOutboxCondition(t, ctx, func() bool {
		row, e := f.q.GetOutboxDeliveryByID(ctx, deliveryID)
		return e == nil && row.State == "delivered"
	})
	var state string
	var attempts int
	if e = f.pool.QueryRow(ctx, "SELECT state,attempts FROM outbox_delivery WHERE event_id=$1", pendingEvent.ID).Scan(&state, &attempts); e != nil || state != "pending" || attempts != 0 {
		t.Fatal("old unsent consumed/marked successful", state, attempts, e)
	}
	select {
	case reason := <-bad:
		t.Fatal(reason)
	default:
	}
	if len(started) != 0 || len(newReceipt) != 0 {
		t.Fatal("duplicate send/old pending actually sent")
	}
	cancel()
	e = <-done
	joined = true
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
