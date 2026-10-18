package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestChannelReplacementAtomicSafeReplayAndHistory(t *testing.T) {
	a, admin, member, root, unit, _, router, _ := setupOperationHTTP(t, func(s *httpapi.ServerHandler, a *app.App) {
		s.SetChannelReplacementService(notify.NewChannelReplacementService(postgres.NewChannelReplacementStore(a.DBPool(), idgen.New()), crypto.NewSecretProtector(make([]byte, 32))))
		s.SetChannelStateService(notify.NewChannelStateService(postgres.NewChannelStateStore(a.DBPool(), idgen.New())))
	})
	ctx := context.Background()
	q := a.Queries()
	call := func(token, method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	created := call(admin, "POST", "/api/v1/notification-channels", "replace-fixture-old", map[string]any{"name": "Old Hook", "channel_type": "webhook", "config": map[string]string{"url": "https://old.example.test/private-old-path", "secret": "private-old-signature"}})
	var details httpapi.NotificationChannelDetails
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &details) != nil {
		t.Fatal(created.Code, created.Body.String())
	}
	old, e := q.GetNotificationChannelByID(ctx, details.Id)
	if e != nil {
		t.Fatal(e)
	}
	ob := outbox.New(a.DBPool(), idgen.New(), clock.New())
	tx, e := a.DBPool().Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	event, e := ob.Publish(ctx, q.WithTx(tx), "fixture", "history", "media.imported", map[string]string{"secret": "private-original-payload"}, []string{"notify:" + old.ID.String()})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var deliveryID uuid.UUID
	if e = a.DBPool().QueryRow(ctx, "SELECT id FROM outbox_delivery WHERE event_id=$1", event.ID).Scan(&deliveryID); e != nil {
		t.Fatal(e)
	}
	before, e := q.GetOutboxDeliveryByID(ctx, deliveryID)
	if e != nil {
		t.Fatal(e)
	}
	file, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root, RelPath: "replace-fixture.mkv", SizeBytes: 8192})
	if e != nil {
		t.Fatal(e)
	}
	if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: unit}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, RootID: pgtype.UUID{Bytes: root, Valid: true}, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, Stage: "imported", QualityProfile: []byte(`{"resolution":"1080p"}`)}); e != nil {
		t.Fatal(e)
	}
	importEvent := func() {
		t.Helper()
		tx, e := a.DBPool().Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = ob.PublishImported(ctx, q.WithTx(tx), unit.String(), map[string]any{"file_id": file.ID, "unit_id": unit}); e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	importEvent()
	path := "/api/v1/notification-channels/" + old.ID.String() + "/replacement"
	command := ports.ChannelReplacementCommand{ExpectedUpdatedAt: old.UpdatedAt.Time, Name: "New Hook", ChannelType: "webhook", Config: ports.NotificationChannelConfig{URL: "https://new.example.test/private-new-path", Secret: "private-new-signature"}, AcceptOldPending: true}
	if w := call(member, "POST", path, "replace-once", command); w.Code != 403 {
		t.Fatal("member replaced", w.Code)
	}
	noAck := command
	noAck.AcceptOldPending = false
	if w := call(admin, "POST", path, "replace-no-ack", noAck); w.Code != 400 {
		t.Fatal("missing old-wait acknowledgement", w.Code)
	}
	stale := command
	stale.ExpectedUpdatedAt = stale.ExpectedUpdatedAt.Add(-time.Microsecond)
	if w := call(admin, "POST", path, "replace-stale", stale); w.Code != 409 {
		t.Fatal("stale stamp replaced", w.Code)
	}
	if _, e = a.DBPool().Exec(ctx, "CREATE FUNCTION fail_replacement_receipt() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key='replace-once' THEN RAISE EXCEPTION 'private-new-signature';END IF;RETURN NEW;END$$;CREATE TRIGGER fail_replacement_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_replacement_receipt()"); e != nil {
		t.Fatal(e)
	}
	if w := call(admin, "POST", path, "replace-once", command); w.Code != 503 || bytes.Contains(w.Body.Bytes(), []byte("private-")) {
		t.Fatal("partial or leaked replacement", w.Code, w.Body.String())
	}
	failed, e := q.GetNotificationChannelByID(ctx, old.ID)
	if e != nil || !failed.IsEnabled || failed.ReplacedBy.Valid || !bytes.Equal(failed.ConfigEncrypted, old.ConfigEncrypted) {
		t.Fatal("old changed despite failed receipt", e)
	}
	var count int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM notification_channel").Scan(&count); e != nil || count != 1 {
		t.Fatal("new channel survived failed transaction", count, e)
	}
	if _, e = a.DBPool().Exec(ctx, "DROP TRIGGER fail_replacement_receipt ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- call(admin, "POST", path, "replace-once", command) }()
	}
	wg.Wait()
	close(results)
	var original string
	var receipt ports.ChannelReplacementReceipt
	for w := range results {
		if w.Code != 201 || w.Header().Get("Cache-Control") != "private, no-store" || bytes.Contains(w.Body.Bytes(), []byte("private-")) {
			t.Fatal(w.Code, w.Body.String())
		}
		if original == "" {
			original = w.Body.String()
			if json.Unmarshal(w.Body.Bytes(), &receipt) != nil {
				t.Fatal("invalid receipt")
			}
		} else if original != w.Body.String() {
			t.Fatal("unstable replay receipt")
		}
	}
	if receipt.NewChannelID == old.ID || receipt.NewChannelID == uuid.Nil || receipt.OldEnabled || !receipt.NewEnabled {
		t.Fatal("wrong replacement receipt", receipt)
	}
	stopped, e := q.GetNotificationChannelByID(ctx, old.ID)
	if e != nil || stopped.IsEnabled || !stopped.ReplacedBy.Valid || uuid.UUID(stopped.ReplacedBy.Bytes) != receipt.NewChannelID || !bytes.Equal(stopped.ConfigEncrypted, old.ConfigEncrypted) {
		t.Fatal("old identity/secret changed", e)
	}
	next, e := q.GetNotificationChannelByID(ctx, receipt.NewChannelID)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := crypto.Decrypt(make([]byte, 32), next.ConfigEncrypted)
	want, _ := json.Marshal(command.Config)
	if e != nil || !bytes.Equal(plain, want) {
		t.Fatal("new config not encrypted with corresponding decryptor", e)
	}
	after, e := q.GetOutboxDeliveryByID(ctx, deliveryID)
	beforeRaw, _ := json.Marshal(before)
	afterRaw, _ := json.Marshal(after)
	if e != nil || !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("historical delivery moved or released", e)
	}
	saved, e := q.GetOutboxEventByID(ctx, event.ID)
	if e != nil || !bytes.Equal(saved.Payload, event.Payload) {
		t.Fatal("old event rewritten", e)
	}
	var oldMembers int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM notification_batch_member WHERE channel_id=$1", old.ID).Scan(&oldMembers); e != nil || oldMembers != 1 {
		t.Fatal("old batch moved", oldMembers, e)
	}
	importEvent()
	var newMembers int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM notification_batch_member WHERE channel_id=$1", next.ID).Scan(&newMembers); e != nil || newMembers != 1 {
		t.Fatal("new received historical source or missed new source", newMembers, e)
	}
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM notification_batch_member WHERE channel_id=$1", old.ID).Scan(&oldMembers); e != nil || oldMembers != 1 {
		t.Fatal("replaced old still received future events", oldMembers, e)
	}
	if d, e := ob.Claim(ctx, "notify:"+old.ID.String(), "must-not-send-old", time.Minute); e != nil || d != nil {
		t.Fatal("replaced recipient still claimable", e)
	}
	enablePath := "/api/v1/notification-channels/" + old.ID.String() + "/state"
	if w := call(admin, "PUT", enablePath, "no-reenable-old", ports.ChannelStateCommand{ExpectedUpdatedAt: stopped.UpdatedAt.Time, IsEnabled: true}); w.Code != 409 {
		t.Fatal("replaced old enabled", w.Code)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE notification_channel SET is_enabled=true WHERE id=$1", old.ID); e == nil {
		t.Fatal("DB enabled replaced old")
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE notification_channel SET config_encrypted=$2 WHERE id=$1", old.ID, next.ConfigEncrypted); e == nil {
		t.Fatal("DB retargeted old credentials")
	}
	againCmd := command
	againCmd.ExpectedUpdatedAt = stopped.UpdatedAt.Time
	if w := call(admin, "POST", path, "no-second-successor", againCmd); w.Code != 409 {
		t.Fatal("replaced channel replaced twice", w.Code)
	}
	changed := command
	changed.Config.Secret = "different-private-signature"
	if w := call(admin, "POST", path, "replace-once", changed); w.Code != 409 {
		t.Fatal("same key secret fingerprint not enforced", w.Code)
	}
	if w := call(member, "POST", path, "replace-once", command); w.Code != 403 {
		t.Fatal("cached receipt elevated member", w.Code)
	}
	if _, e = q.DisableNotificationChannel(ctx, next.ID); e != nil {
		t.Fatal(e)
	}
	if w := call(admin, "POST", path, "replace-once", command); w.Code != 201 || w.Body.String() != original {
		t.Fatal("replayed result returned live new state", w.Code, w.Body.String())
	}
	var auditCount int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='notification_channel.replaced'").Scan(&auditCount); e != nil || auditCount != 1 {
		t.Fatal("duplicate audit", auditCount, e)
	}
	var leaked bool
	if e = a.DBPool().QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM idempotency_record WHERE response_body::text LIKE '%private-new-%') OR EXISTS(SELECT 1 FROM audit_log WHERE payload::text LIKE '%private-new-%')").Scan(&leaked); e != nil || leaked {
		t.Fatal("plaintext credentials in cache/audit", e)
	}
	if w := call(admin, "GET", "/api/v1/notification-channels", "", nil); w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("private-")) || !bytes.Contains(w.Body.Bytes(), []byte(receipt.NewChannelID.String())) {
		t.Fatal("safe replacement list missing", w.Code, w.Body.String())
	}
	if _, e = a.DBPool().Exec(ctx, "DELETE FROM notification_channel WHERE id=$1", old.ID); e == nil {
		t.Fatal("replaced old deleted")
	}
	if _, e = a.DBPool().Exec(ctx, "DELETE FROM notification_channel WHERE id=$1", next.ID); e == nil {
		t.Fatal("successor deleted")
	}
	dsn := a.DBPool().Config().ConnConfig.ConnString()
	for version := postgres.CurrentMigrationVersion; version > 35; version-- {
		if _, e = postgres.Rollback(ctx, dsn, "../../migrations"); e != nil {
			t.Fatal("unrelated empty migration cannot be undone", version, e)
		}
	}
	if _, e = postgres.Rollback(ctx, dsn, "../../migrations"); e == nil {
		t.Fatal("replacement evidence discarded by downgrade")
	}
	if e = postgres.CheckSchemaVersion(ctx, a.DBPool(), 35); e != nil {
		t.Fatal("protected downgrade partially applied", e)
	}
	if _, e = postgres.Migrate(ctx, dsn, "../../migrations"); e != nil {
		t.Fatal(e)
	}
}
