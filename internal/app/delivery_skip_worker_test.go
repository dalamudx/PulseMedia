package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeliverySkipAppRunUnblocksOriginalMediaSuccessor(t *testing.T) {
	f := organizationSetup(t)
	ob, _, n := testOutboxServices(f)
	receipts := make(chan string, 4)
	bad := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("private-hmac-secret"))
		mac.Write(raw)
		if r.Header.Get("X-PulseMedia-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			bad <- "bad HMAC"
		}
		receipts <- r.Header.Get("X-PulseMedia-Event-ID")
		w.WriteHeader(204)
	}))
	defer server.Close()
	ch := createOutboxChannel(t, f, n, server.URL)
	importForOutbox(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	legacy, e := ob.Publish(ctx, f.q.WithTx(tx), "media", f.unit.String(), "media.imported", map[string]string{"fixture": "historical evidence"}, []string{"notify:" + ch.String()})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var id uuid.UUID
	if e = f.pool.QueryRow(ctx, "UPDATE outbox_delivery SET state='dead',attempts=5 WHERE event_id=$1 RETURNING id", legacy.ID).Scan(&id); e != nil {
		t.Fatal(e)
	}
	old, e := f.q.GetOutboxDeliveryByID(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if claim, e := ob.Claim(ctx, "notify:"+ch.String(), "blocked", time.Minute); e != nil || claim != nil {
		t.Fatal("old unit dead barrier bypassed", claim, e)
	}
	skip := notify.NewDeliverySkipService(postgres.NewDeliveryManagementStore(f.pool, f.gen))
	if _, e = skip.Skip(ctx, notificationFixtureAdmin(t, f), id, ports.DeliverySkipCommand{ExpectedToken: old.LeaseToken, ExpectedAttempts: int(old.Attempts), ExpectedUpdatedAt: old.UpdatedAt.Time, AcceptDeliveryLoss: true, Reason: "event_obsolete"}, "legacy-audited-skip"); e != nil {
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
		e := f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM outbox_delivery WHERE consumer=$1 AND state='delivered')", "notify:"+ch.String()).Scan(&delivered)
		return e == nil && delivered
	})
	if len(receipts) != 1 {
		t.Fatal("skip sent or lost successor", len(receipts))
	}
	got := <-receipts
	if got == legacy.ID.String() {
		t.Fatal("skipped event actually sent")
	}
	var eventID string
	if e = f.pool.QueryRow(ctx, "SELECT event_id::text FROM notification_batch WHERE channel_id=$1", ch).Scan(&eventID); e != nil || got != eventID {
		t.Fatal("retargeted/rebuilt successor", e)
	}
	after, e := f.q.GetOutboxDeliveryByID(ctx, id)
	if e != nil || after.State != "skipped" || after.DeliveredAt.Valid || !after.SkippedAt.Valid {
		t.Fatal("skip changed into delivered", e)
	}
	select {
	case reason := <-bad:
		t.Fatal(reason)
	default:
	}
	cancel()
	e = <-done
	joined = true
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
