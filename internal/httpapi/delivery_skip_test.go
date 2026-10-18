package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDeliverySkipAtomicFenceAndLossAck(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(s *httpapi.ServerHandler, a *app.App) {
		s.SetDeliverySkipService(notify.NewDeliverySkipService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
		s.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.DBPool(), idgen.New())))
	})
	ctx := context.Background()
	q := a.Queries()
	ob := outbox.New(a.DBPool(), idgen.New(), clock.New())
	channel, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: uuid.New(), Name: "Skip Hook", ChannelType: "webhook", ConfigEncrypted: []byte("private-fixture-not-a-network-configuration")})
	if e != nil {
		t.Fatal(e)
	}
	consumer := "notify:" + channel.ID.String()
	tx, e := a.DBPool().Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	first, e := ob.Publish(ctx, q.WithTx(tx), "notification.media", "fixture", "notification.fixture", map[string]string{"secret": "private-fixture-secret"}, []string{consumer})
	if e != nil {
		t.Fatal(e)
	}
	_, e = ob.Publish(ctx, q.WithTx(tx), "notification.media", "fixture", "notification.fixture", map[string]string{"fixture": "second"}, []string{consumer})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var id uuid.UUID
	if e = a.DBPool().QueryRow(ctx, "UPDATE outbox_delivery SET state='dead',attempts=5 WHERE event_id=$1 RETURNING id", first.ID).Scan(&id); e != nil {
		t.Fatal(e)
	}
	d, e := q.GetOutboxDeliveryByID(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	command := ports.DeliverySkipCommand{ExpectedToken: d.LeaseToken, ExpectedAttempts: int(d.Attempts), ExpectedUpdatedAt: d.UpdatedAt.Time, AcceptDeliveryLoss: true, Reason: "event_obsolete"}
	path := "/api/v1/integrations/deliveries/" + id.String() + "/skip"
	call := func(token, key string, c ports.DeliverySkipCommand) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(c)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := call(member, "skip-once", command); w.Code != 403 {
		t.Fatal("member skipped", w.Code)
	}
	noAck := command
	noAck.AcceptDeliveryLoss = false
	if w := call(admin, "skip-no-ack", noAck); w.Code != 400 {
		t.Fatal("missing loss acknowledgement", w.Code)
	}
	if next, e := ob.Claim(ctx, consumer, "blocked", time.Minute); e != nil || next != nil {
		t.Fatal("dead predecessor not blocking", next, e)
	}
	if _, e = a.DBPool().Exec(ctx, "CREATE FUNCTION fail_skip_receipt() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key='skip-once' THEN RAISE EXCEPTION 'private-fixture-secret';END IF;RETURN NEW;END $$;CREATE TRIGGER fail_skip_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_skip_receipt()"); e != nil {
		t.Fatal(e)
	}
	if w := call(admin, "skip-once", command); w.Code != 503 || bytes.Contains(w.Body.Bytes(), []byte("private-fixture-secret")) {
		t.Fatal("failure leaked or partial write", w.Code, w.Body.String())
	}
	after, e := q.GetOutboxDeliveryByID(ctx, id)
	if e != nil || after.State != "dead" || after.SkippedAt.Valid || after.LeaseToken != d.LeaseToken {
		t.Fatal("skip escaped failed receipt transaction", after, e)
	}
	if _, e = a.DBPool().Exec(ctx, "DROP TRIGGER fail_skip_receipt ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	if _, e = q.DisableNotificationChannel(ctx, channel.ID); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- call(admin, "skip-once", command) }()
	}
	wg.Wait()
	close(results)
	var original string
	for w := range results {
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if original == "" {
			original = w.Body.String()
		} else if original != w.Body.String() {
			t.Fatal("unstable skip receipt")
		}
	}
	after, e = q.GetOutboxDeliveryByID(ctx, id)
	if e != nil || after.State != "skipped" || after.DeliveredAt.Valid || !after.SkippedAt.Valid || after.EventID != d.EventID || after.Consumer != d.Consumer || after.Seq != d.Seq || after.LeaseOwner.Valid || after.LeaseUntil.Valid || after.LeaseToken != d.LeaseToken+1 {
		t.Fatal("skip fabricated delivery or changed identity", after, e)
	}
	var audits int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='integration.delivery_skipped'").Scan(&audits); e != nil || audits != 1 {
		t.Fatal("concurrent skips duplicated audit", audits, e)
	}
	if w := call(member, "skip-once", command); w.Code != 403 {
		t.Fatal("receipt elevated member", w.Code)
	}
	changed := command
	changed.Reason = "recipient_retired"
	if w := call(admin, "skip-once", changed); w.Code != 409 {
		t.Fatal("fingerprint change accepted", w.Code)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE outbox_delivery SET state='pending' WHERE id=$1", id); e == nil {
		t.Fatal("audited skip reactivated")
	}
	if _, e = q.ReplayDeadIntegrationDelivery(ctx, db.ReplayDeadIntegrationDeliveryParams{ID: id}); e == nil {
		t.Fatal("skipped replayed as dead")
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE notification_channel SET is_enabled=true WHERE id=$1", channel.ID); e != nil {
		t.Fatal(e)
	}
	next, e := ob.Claim(ctx, consumer, "unblocked", time.Minute)
	if e != nil || next == nil || next.EventID == first.ID {
		t.Fatal("audited skip did not unblock successor", next, e)
	}
	for v := int64(postgres.CurrentMigrationVersion); v > 33; v-- {
		if got, e := postgres.Rollback(ctx, a.DBPool().Config().ConnConfig.ConnString(), "../../migrations"); e != nil || got != v-1 {
			t.Fatal("empty channel replacement migration down", got, e)
		}
	}
	if _, e = postgres.Rollback(ctx, a.DBPool().Config().ConnConfig.ConnString(), "../../migrations"); e == nil {
		t.Fatal("skip reason/history removed by downgrade")
	}
	if _, e = postgres.Migrate(ctx, a.DBPool().Config().ConnConfig.ConnString(), "../../migrations"); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DBPool().Exec(ctx, "INSERT INTO outbox_delivery(id,event_id,consumer,aggregate_type,aggregate_id,seq,state,skipped_at) VALUES($1,$2,'notify:invalid','fixture','invalid',1,'skipped',clock_timestamp())", uuid.New(), first.ID); e == nil {
		t.Fatal("SQL null reason accepted")
	}
	running := command
	running.ExpectedToken = next.LeaseToken
	running.ExpectedAttempts = int(next.Attempts)
	running.ExpectedUpdatedAt = next.UpdatedAt.Time
	path = "/api/v1/integrations/deliveries/" + next.ID.String() + "/skip"
	if w := call(admin, "do-not-cancel-running", running); w.Code != 409 {
		t.Fatal("skip released running delivery", w.Code)
	}
	still, e := q.GetOutboxDeliveryByID(ctx, next.ID)
	if e != nil || still.State != "delivering" || !still.LeaseOwner.Valid {
		t.Fatal("running physical execution lease released", e)
	}
}
