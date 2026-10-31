package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestNotificationLongRetrySurvivesRecoveryPG(t *testing.T) {
	f := organizationSetup(t)
	ob, _, n := testOutboxServices(f)
	var mu sync.Mutex
	var events []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, r.Header.Get("X-PulseMedia-Event-ID"))
		if len(events) == 1 {
			w.Header().Set("Retry-After", "172800")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	channel := createOutboxChannel(t, f, n, server.URL)
	consumer := "notify:" + channel.String()
	ctx := t.Context()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	event, err := ob.Publish(ctx, db.New(tx), "library", uuid.NewString(), "media.imported", nil, []string{consumer})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	delivery, err := ob.Claim(ctx, consumer, "first", time.Minute)
	if err != nil || delivery == nil {
		t.Fatal("claim", err)
	}
	var before time.Time
	if err = f.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = n.DispatchDelivery(ctx, delivery.ID, channel, "first", delivery.LeaseToken); err == nil {
		t.Fatal("429 accepted as delivered")
	}
	var state string
	var attempts int
	var deadline, after time.Time
	if err = f.pool.QueryRow(ctx, "SELECT state,attempts,not_before,clock_timestamp() FROM outbox_delivery WHERE id=$1", delivery.ID).Scan(&state, &attempts, &deadline, &after); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempts != 1 || deadline.Before(before.Add(24*time.Hour)) || deadline.After(after.Add(24*time.Hour)) {
		t.Fatalf("state=%s attempts=%d remaining=%s", state, attempts, deadline.Sub(after))
	}
	// Recreate the services, as on restart; recovery must respect persisted advice.
	restarted, _, dispatcher := testOutboxServices(f)
	if _, err = f.q.RecoverExpiredOutboxDeliveries(ctx, consumer); err != nil {
		t.Fatal(err)
	}
	consumers, err := f.q.ListRuntimeOutboxConsumers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range consumers {
		if candidate == consumer {
			t.Fatal("future delivery scheduled")
		}
	}
	if early, err := restarted.Claim(ctx, consumer, "restart", time.Minute); err != nil || early != nil {
		t.Fatal("premature retry", err)
	}
	// Advance only this fixture's eligibility, then deliver the original event.
	if _, err = f.pool.Exec(ctx, "UPDATE outbox_delivery SET not_before=clock_timestamp()-interval '1 second' WHERE id=$1", delivery.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := restarted.Claim(ctx, consumer, "restart", time.Minute)
	if err != nil || retry == nil {
		t.Fatal("due retry", err)
	}
	if retry.ID != delivery.ID || retry.EventID != event.ID || retry.LeaseToken <= delivery.LeaseToken {
		t.Fatal("retry identity changed")
	}
	if err = dispatcher.DispatchDelivery(ctx, retry.ID, channel, "restart", retry.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, "SELECT state,attempts FROM outbox_delivery WHERE id=$1", delivery.ID).Scan(&state, &attempts); err != nil || state != "delivered" || attempts != 2 {
		t.Fatal(state, attempts, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0] != event.ID.String() || events[1] != events[0] {
		t.Fatal("lost stable event identity", events)
	}
}
