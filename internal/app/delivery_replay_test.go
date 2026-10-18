package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppRunReplayedDeliveryPreservesEventHMAC(t *testing.T) {
	f := organizationSetup(t)
	_, _, n := testOutboxServices(f)
	ctx := context.Background()
	eventID := uuid.New()
	var sends atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(r.Body)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		mac := hmac.New(sha256.New, []byte("private-hmac-secret"))
		mac.Write(body)
		if r.Header.Get("X-PulseMedia-Event-ID") != eventID.String() || r.Header.Get("X-PulseMedia-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("replay retargeted event/signature")
			w.WriteHeader(403)
			return
		}
		var payload ports.Notification
		if e = json.Unmarshal(body, &payload); e != nil || payload.Title != "PulseMedia: media update" || payload.Body != "Media imported into library (event "+eventID.String()+")" {
			t.Error("legacy replay changed the previously emitted title/body", e)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sends.Add(1)
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	channel := createOutboxChannel(t, f, n, receiver.URL)
	agg := uuid.NewString()
	if _, e := f.q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: eventID, AggregateType: "media", AggregateID: agg, EventType: "media.imported", Seq: 1, Payload: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	d, e := f.q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: uuid.New(), EventID: eventID, Consumer: "notify:" + channel.String(), AggregateType: "media", AggregateID: agg, Seq: 1, MaxAttempts: 2})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE outbox_delivery SET state='dead',attempts=2 WHERE id=$1", d.ID); e != nil {
		t.Fatal(e)
	}
	d, e = f.q.GetOutboxDeliveryByID(ctx, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	management := notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(f.pool, f.gen))
	receipt, e := management.Replay(ctx, notificationFixtureAdmin(t, f), d.ID, ports.DeliveryReplayCommand{ExpectedToken: d.LeaseToken, ExpectedAttempts: int(d.Attempts), ExpectedUpdatedAt: d.UpdatedAt.Time, AcceptDuplicateRisk: true}, "runtime-replay")
	if e != nil || receipt.EventID != eventID {
		t.Fatal(receipt, e)
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
	work, cancel := context.WithTimeout(ctx, 8*time.Second)
	done := make(chan error, 1)
	go func() { done <- a.Run(work) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	waitOutboxCondition(t, work, func() bool {
		saved, e := f.q.GetOutboxDeliveryByID(work, d.ID)
		return e == nil && saved.State == "delivered"
	})
	if sends.Load() != 1 {
		t.Fatal("replayed delivery did not send exactly once in controlled fixture", sends.Load())
	}
	saved, e := f.q.GetOutboxDeliveryByID(ctx, d.ID)
	if e != nil || saved.EventID != eventID || saved.Consumer != d.Consumer || saved.Seq != d.Seq {
		t.Fatal("replay replaced recipient/event/order", saved, e)
	}
}
