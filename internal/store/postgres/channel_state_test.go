package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func TestChannelStateAtomicFenceAndDisabledBudget(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := postgres.Migrate(ctx, dsn, migrationsDir); e != nil {
		t.Fatal(e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	q := postgres.Queries(pool)
	gen := idgen.New()
	ob := outbox.New(pool, gen, clock.New())
	svc := notify.NewChannelStateService(postgres.NewChannelStateStore(pool, gen))
	user, e := q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "channel-state-admin", PasswordHash: "fixture", Role: "admin", IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.ID.String(), Role: ports.RoleAdmin}
	cipher, e := crypto.Encrypt(bytes.Repeat([]byte{1}, 32), []byte(`{"url":"http://localhost/private-token","secret":"fixture-private-hmac"}`))
	if e != nil {
		t.Fatal(e)
	}
	ch, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: uuid.New(), Name: "Fixture", ChannelType: "webhook", ConfigEncrypted: cipher})
	if e != nil {
		t.Fatal(e)
	}
	consumer := "notify:" + ch.ID.String()
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	event, e := ob.Publish(ctx, q.WithTx(tx), "media", "fixture", "media.imported", map[string]string{"id": "fixture"}, []string{consumer})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	owned, e := ob.Claim(ctx, consumer, "owner", time.Minute)
	if e != nil || owned == nil {
		t.Fatal(e)
	}
	command := ports.ChannelStateCommand{ExpectedUpdatedAt: ch.UpdatedAt.Time, IsEnabled: false}
	if _, e = pool.Exec(ctx, "CREATE FUNCTION fail_channel_state() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key='state-once' THEN RAISE EXCEPTION 'fixture-private-hmac';END IF;RETURN NEW;END $$;CREATE TRIGGER fail_channel_state BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_channel_state()"); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Set(ctx, actor, ch.ID, command, "state-once"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("missing final receipt rollback", e)
	}
	after, e := q.GetNotificationChannelByID(ctx, ch.ID)
	if e != nil || !after.IsEnabled || !after.UpdatedAt.Time.Equal(ch.UpdatedAt.Time) {
		t.Fatal("partial state committed", e)
	}
	var count int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='notification_channel.state_changed'").Scan(&count); e != nil || count != 0 {
		t.Fatal("audit leaked past rollback", count, e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER fail_channel_state ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := svc.Set(ctx, actor, ch.ID, command, "state-once"); results <- e }()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal("unstable concurrent recovery", e)
		}
	}
	disabled, e := q.GetNotificationChannelByID(ctx, ch.ID)
	if e != nil || disabled.IsEnabled || !disabled.UpdatedAt.Time.After(ch.UpdatedAt.Time) || !bytes.Equal(disabled.ConfigEncrypted, cipher) {
		t.Fatal("cipher/destination or fence changed", e)
	}
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='notification_channel.state_changed'").Scan(&count); e != nil || count != 1 {
		t.Fatal("eight submissions became eight commands", count, e)
	}
	if _, e = svc.Set(ctx, actor, ch.ID, command, "stale-state"); !errors.Is(e, ports.ErrChannelStateConflict) {
		t.Fatal("old version accepted", e)
	}
	if _, e = svc.Set(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleMember}, ch.ID, command, "state-once"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("cached admin state elevated member", e)
	}
	if e = ob.ReturnDisabledNotification(ctx, owned.ID, consumer, "other", owned.LeaseToken); !errors.Is(e, outbox.ErrStaleDeliveryLease) {
		t.Fatal("foreign owner released lease", e)
	}
	if e = ob.ReturnDisabledNotification(ctx, owned.ID, consumer, "owner", owned.LeaseToken); e != nil {
		t.Fatal(e)
	}
	pending, e := q.GetOutboxDeliveryByID(ctx, owned.ID)
	if e != nil || pending.State != "pending" || pending.Attempts != 0 || pending.EventID != event.ID || pending.Seq != owned.Seq || pending.LeaseOwner.Valid || pending.LeaseUntil.Valid {
		t.Fatal("disabled delivery spent budget/changed identity", pending, e)
	}
	if e = ob.Finish(ctx, owned.ID, "owner", owned.LeaseToken); !errors.Is(e, outbox.ErrStaleDeliveryLease) {
		t.Fatal("old token finished paused delivery", e)
	}
	for range 4 {
		d, e := ob.Claim(ctx, consumer, "new-owner", time.Minute)
		if e != nil || d != nil {
			t.Fatal("disabled recipient consumed pending attempts", d, e)
		}
	}
	enabled, e := svc.Set(ctx, actor, ch.ID, ports.ChannelStateCommand{ExpectedUpdatedAt: disabled.UpdatedAt.Time, IsEnabled: true}, "resume-state")
	if e != nil || !enabled.IsEnabled {
		t.Fatal(e)
	}
	resumed, e := ob.Claim(ctx, consumer, "resumed", time.Minute)
	if e != nil || resumed == nil || resumed.EventID != event.ID || resumed.Attempts != 1 {
		t.Fatal("resume retargeted/lost queued event", resumed, e)
	}
	if e = ob.ReturnDisabledNotification(ctx, resumed.ID, consumer, "resumed", resumed.LeaseToken); !errors.Is(e, outbox.ErrStaleDeliveryLease) {
		t.Fatal("enabled recipient allowed pause", e)
	}
	if e = ob.Finish(ctx, resumed.ID, "resumed", resumed.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if receipt, e := svc.Set(ctx, actor, ch.ID, command, "state-once"); e != nil || receipt.IsEnabled || !receipt.UpdatedAt.Equal(disabled.UpdatedAt.Time) {
		t.Fatal("recovery returned live state instead of original receipt", receipt, e)
	}
}
