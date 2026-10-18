package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestNotificationManagementCurrentAccountAndReceiptProtection(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	_, _, n := testOutboxServices(f)
	a := notificationFixtureAdmin(t, f)
	cfg := notify.ChannelConfig{URL: "https://example.test/local-fixture", Secret: "synthetic-secret"}
	ch, e := n.CreateChannel(ctx, a, "Original", "webhook", cfg, "created")
	if e != nil {
		t.Fatal(e)
	}
	state := notify.NewChannelStateService(postgres.NewChannelStateStore(f.pool, f.gen))
	stateCmd := ports.ChannelStateCommand{ExpectedUpdatedAt: ch.UpdatedAt, IsEnabled: false}
	sr, e := state.Set(ctx, a, ch.ID, stateCmd, "disabled")
	if e != nil {
		t.Fatal(e)
	}
	replacement := notify.NewChannelReplacementService(postgres.NewChannelReplacementStore(f.pool, f.gen), crypto.NewSecretProtector(outboxTestKey))
	replaceCmd := ports.ChannelReplacementCommand{ExpectedUpdatedAt: sr.UpdatedAt, Name: "New", ChannelType: "webhook", Config: ports.NotificationChannelConfig{URL: cfg.URL, Secret: cfg.Secret}, AcceptOldPending: true}
	rr, e := replacement.Replace(ctx, a, ch.ID, replaceCmd, "replaced")
	if e != nil {
		t.Fatal(e)
	}
	delivery := notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(f.pool, f.gen))
	skip := notify.NewDeliverySkipService(postgres.NewDeliveryManagementStore(f.pool, f.gen))
	batches := notify.NewBatchQueryService(postgres.NewNotificationBatchQueryStore(f.pool))
	dead := func() db.OutboxDelivery {
		t.Helper()
		ev, e := f.q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: uuid.New(), AggregateType: "media", AggregateID: uuid.NewString(), EventType: "media.imported", Seq: 1, Payload: []byte("{}")})
		if e != nil {
			t.Fatal(e)
		}
		d, e := f.q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: uuid.New(), EventID: ev.ID, Consumer: "notify:" + rr.NewChannelID.String(), AggregateType: ev.AggregateType, AggregateID: ev.AggregateID, Seq: 1, MaxAttempts: 3})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.pool.Exec(ctx, "UPDATE outbox_delivery SET state='dead',attempts=3 WHERE id=$1", d.ID); e != nil {
			t.Fatal(e)
		}
		d, e = f.q.GetOutboxDeliveryByID(ctx, d.ID)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	d := dead()
	replayCmd := ports.DeliveryReplayCommand{ExpectedToken: d.LeaseToken, ExpectedAttempts: int(d.Attempts), ExpectedUpdatedAt: d.UpdatedAt.Time, AcceptDuplicateRisk: true}
	dr, e := delivery.Replay(ctx, a, d.ID, replayCmd, "replayed")
	if e != nil {
		t.Fatal(e)
	}
	ds := dead()
	skipCmd := ports.DeliverySkipCommand{ExpectedToken: ds.LeaseToken, ExpectedAttempts: int(ds.Attempts), ExpectedUpdatedAt: ds.UpdatedAt.Time, AcceptDeliveryLoss: true, Reason: "event_obsolete"}
	kr, e := skip.Skip(ctx, a, ds.ID, skipCmd, "skipped")
	if e != nil {
		t.Fatal(e)
	}
	probes := []struct {
		name string
		run  func(*ports.Actor) error
	}{
		{"channels", func(a *ports.Actor) error { _, e := n.ListChannels(ctx, a); return e }},
		{"batches", func(a *ports.Actor) error { _, e := batches.List(ctx, a); return e }},
		{"deliveries", func(a *ports.Actor) error { _, e := delivery.List(ctx, a); return e }},
		{"create replay", func(a *ports.Actor) error {
			_, e := n.CreateChannel(ctx, a, "Original", "webhook", cfg, "created")
			return e
		}},
		{"new create", func(a *ports.Actor) error {
			_, e := n.CreateChannel(ctx, a, "Other", "webhook", cfg, "new-create")
			return e
		}},
		{"state replay", func(a *ports.Actor) error { _, e := state.Set(ctx, a, ch.ID, stateCmd, "disabled"); return e }},
		{"replace replay", func(a *ports.Actor) error {
			_, e := replacement.Replace(ctx, a, ch.ID, replaceCmd, "replaced")
			return e
		}},
		{"delivery replay", func(a *ports.Actor) error { _, e := delivery.Replay(ctx, a, d.ID, replayCmd, "replayed"); return e }},
		{"skip replay", func(a *ports.Actor) error { _, e := skip.Skip(ctx, a, ds.ID, skipCmd, "skipped"); return e }},
	}
	snapshot := func() string {
		t.Helper()
		var v string
		e := f.pool.QueryRow(ctx, `SELECT jsonb_build_object('channels',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM notification_channel c),'deliveries',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM outbox_delivery d),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM outbox_event e),'audits',(SELECT count(*) FROM audit_log),'receipts',(SELECT count(*) FROM idempotency_record),'tasks',(SELECT count(*) FROM download_task),'protection',(SELECT count(*) FROM acquisition_reservation))::text`).Scan(&v)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	before := snapshot()
	check := func(a *ports.Actor) {
		t.Helper()
		for _, p := range probes {
			if e := p.run(a); !errors.Is(e, ports.ErrForbidden) {
				t.Fatalf("%s did not reject current account: %v", p.name, e)
			}
		}
		if snapshot() != before {
			t.Fatal("denied notification command changed historical or physical records")
		}
	}
	for _, change := range []string{"is_active=false", "role='member'"} {
		if _, e = f.pool.Exec(ctx, "UPDATE app_user SET "+change+" WHERE id=$1", uuid.MustParse(a.ID)); e != nil {
			t.Fatal(e)
		}
		check(a)
		if _, e = f.pool.Exec(ctx, "UPDATE app_user SET is_active=true,role='admin' WHERE id=$1", uuid.MustParse(a.ID)); e != nil {
			t.Fatal(e)
		}
	}
	for _, bad := range []*ports.Actor{{Type: ports.ActorTypeUser, ID: a.ID, Role: ports.RoleAdmin, Scopes: []string{"read"}}, {Type: ports.ActorTypeSystem, ID: a.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}} {
		check(bad)
	}
	if got, e := replacement.Replace(ctx, a, ch.ID, replaceCmd, "replaced"); e != nil || *got != *rr {
		t.Fatal("replacement receipt lost after restored authorization", e)
	}
	if got, e := delivery.Replay(ctx, a, d.ID, replayCmd, "replayed"); e != nil || *got != *dr {
		t.Fatal("delivery receipt lost after restored authorization", e)
	}
	if got, e := skip.Skip(ctx, a, ds.ID, skipCmd, "skipped"); e != nil || *got != *kr {
		t.Fatal("skip receipt lost after restored authorization", e)
	}
	if _, e = f.pool.Exec(ctx, "DELETE FROM app_user WHERE id=$1", uuid.MustParse(a.ID)); e != nil {
		t.Fatal(e)
	}
	check(a)
}

func TestNotificationStateRejectsUserChangeAfterLockWait(t *testing.T) {
	f := organizationSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	a := notificationFixtureAdmin(t, f)
	_, _, n := testOutboxServices(f)
	ch, e := n.CreateChannel(ctx, a, "Current", "webhook", notify.ChannelConfig{URL: "https://example.test/local", Secret: "synthetic"}, "current")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(a.ID)); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := postgres.NewChannelStateStore(f.pool, f.gen).SetChannelState(ctx, a, ch.ID, ports.ChannelStateCommand{ExpectedUpdatedAt: ch.UpdatedAt, IsEnabled: false}, "waited", strings.Repeat("a", 64))
		done <- e
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if e = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%' AND query LIKE '%FOR SHARE%')").Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("operation did not wait for current user: %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("stale admin passed lock wait", e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	row, e := f.q.GetNotificationChannelByID(ctx, ch.ID)
	if e != nil || !row.IsEnabled || !row.UpdatedAt.Time.Equal(ch.UpdatedAt) {
		t.Fatal("rejected change modified channel", e)
	}
	var count int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM idempotency_record WHERE idempotency_key='waited'").Scan(&count); e != nil || count != 0 {
		t.Fatal("rejected change recorded receipt", e)
	}
}
