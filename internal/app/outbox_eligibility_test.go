package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestOutboxDiscoveryRequiresExecutableOrRecoverableDeliveryPG(t *testing.T) {
	f := organizationSetup(t)
	ob, _, n := testOutboxServices(f)
	ctx := context.Background()
	channel := createOutboxChannel(t, f, n, "http://127.0.0.1:1/unused")
	consumer := "notify:" + channel.String()
	publish := func(kind, aggregate string) uuid.UUID {
		t.Helper()
		tx, e := f.pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		event, e := ob.Publish(ctx, db.New(tx), kind, aggregate, "probe", nil, []string{consumer})
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		var id uuid.UUID
		if e = f.pool.QueryRow(ctx, "SELECT id FROM outbox_delivery WHERE event_id=$1", event.ID).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := f.pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	discovered := func(want bool) {
		t.Helper()
		rows, e := f.q.ListRuntimeOutboxConsumers(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if slices.Contains(rows, consumer) != want {
			t.Errorf("discovered=%v want=%v", slices.Contains(rows, consumer), want)
		}
	}
	first := publish("probe", "ordered")
	discovered(true)
	exec("UPDATE notification_channel SET is_enabled=false WHERE id=$1", channel)
	discovered(false)
	exec("UPDATE notification_channel SET is_enabled=true WHERE id=$1", channel)
	discovered(true)
	exec("UPDATE outbox_delivery SET state='dead' WHERE id=$1", first)
	second := publish("probe", "ordered")
	discovered(false)
	independent := publish("probe", "independent")
	discovered(true)
	d, e := ob.Claim(ctx, consumer, "owner", time.Minute)
	if e != nil || d == nil || d.ID != independent {
		t.Fatalf("independent claim: %v", e)
	}
	if e = ob.Finish(ctx, d.ID, "owner", d.LeaseToken); e != nil {
		t.Fatal(e)
	}
	discovered(false)
	exec("UPDATE outbox_delivery SET state='skipped',skipped_at=clock_timestamp(),skip_reason='event_obsolete' WHERE id=$1", first)
	discovered(true)
	exec("UPDATE outbox_delivery SET attempts=max_attempts WHERE id=$1", second)
	discovered(false)
	exec("UPDATE outbox_delivery SET attempts=0,not_before=clock_timestamp()+interval '1 hour' WHERE id=$1", second)
	discovered(false)
	exec("UPDATE outbox_delivery SET not_before=clock_timestamp()-interval '1 second' WHERE id=$1", second)
	discovered(true)
	d, e = ob.Claim(ctx, consumer, "owner", time.Minute)
	if e != nil || d == nil {
		t.Fatalf("claim: %v", e)
	}
	exec("UPDATE notification_channel SET is_enabled=false WHERE id=$1", channel)
	exec("UPDATE outbox_delivery SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", second)
	discovered(true)
	if _, e = f.q.RecoverExpiredOutboxDeliveries(ctx, consumer); e != nil {
		t.Fatal(e)
	}
	discovered(false)
	exec("UPDATE notification_channel SET is_enabled=true WHERE id=$1", channel)
	discovered(true)
	d, e = ob.Claim(ctx, consumer, "new-owner", time.Minute)
	if e != nil || d == nil || d.LeaseToken <= 1 {
		t.Fatalf("recovery claim: %v", e)
	}
	if e = ob.Finish(ctx, d.ID, "new-owner", d.LeaseToken); e != nil {
		t.Fatal(e)
	}
	discovered(false)
	legacy := publish("media", f.unit.String())
	exec("UPDATE outbox_delivery SET state='dead' WHERE id=$1", legacy)
	publish("notification.media", f.media.String())
	discovered(false)
	exec("UPDATE outbox_delivery SET state='skipped',skipped_at=clock_timestamp(),skip_reason='event_obsolete' WHERE id=$1", legacy)
	discovered(true)
}
