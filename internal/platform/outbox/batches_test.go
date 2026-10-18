package outbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"sync"
	"testing"
	"time"
)

func TestNotificationBatchSnapshotRollbackSealAndBound(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if v, e := postgres.Migrate(ctx, dsn, migrationsDir); e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	q := postgres.Queries(pool)
	ob := outbox.New(pool, idgen.New(), clock.New())
	m, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "Fixture", MediaType: "tv"})
	if e != nil {
		t.Fatal(e)
	}
	u, e := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: m.ID, UnitKey: "S02E03"})
	if e != nil {
		t.Fatal(e)
	}
	files := []uuid.UUID{}
	for range 2 {
		r, e := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "tv", Name: "Fixture"})
		if e != nil {
			t.Fatal(e)
		}
		f, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: r.ID, RelPath: "Fixture.mkv", SizeBytes: 8192})
		if e != nil {
			t.Fatal(e)
		}
		if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: f.ID, UnitID: u.ID}); e != nil {
			t.Fatal(e)
		}
		if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: u.ID, Stage: "imported", RootID: pgtype.UUID{Bytes: r.ID, Valid: true}, SourceFileID: pgtype.UUID{Bytes: f.ID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p"}`)}); e != nil {
			t.Fatal(e)
		}
		files = append(files, f.ID)
	}
	channel := func() uuid.UUID {
		t.Helper()
		ch, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: uuid.New(), Name: "Fixture", ChannelType: "webhook", ConfigEncrypted: []byte("fixture-only-not-a-real-config")})
		if e != nil {
			t.Fatal(e)
		}
		return ch.ID
	}
	a := channel()
	publish := func(file uuid.UUID) (db.OutboxEvent, error) {
		tx, e := pool.Begin(ctx)
		if e != nil {
			return db.OutboxEvent{}, e
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		event, e := ob.PublishImported(ctx, q.WithTx(tx), u.ID.String(), map[string]any{"unit_id": u.ID, "file_id": file, "target_path": "/fixture/private-notify-token"})
		if e != nil {
			return db.OutboxEvent{}, e
		}
		e = tx.Commit(ctx)
		return *event, e
	}
	first, e := publish(files[0])
	if e != nil {
		t.Fatal(e)
	}
	b := channel()
	second, e := publish(files[1])
	if e != nil {
		t.Fatal(e)
	}
	var aCount, bCount int
	if e = pool.QueryRow(ctx, "SELECT member_count FROM notification_batch WHERE channel_id=$1", a).Scan(&aCount); e != nil || aCount != 2 {
		t.Fatal("missing shared media grouping", aCount, e)
	}
	if e = pool.QueryRow(ctx, "SELECT member_count FROM notification_batch WHERE channel_id=$1", b).Scan(&bCount); e != nil || bCount != 1 {
		t.Fatal("new recipient retroactively received old event", bCount, e)
	}
	if n, e := ob.SealDueNotificationBatches(ctx, 32); e != nil || n != 0 {
		t.Fatal("fixed window ignored", n, e)
	}
	if _, e = pool.Exec(ctx, "UPDATE notification_batch SET state='ready' WHERE state='open'; CREATE FUNCTION reject_batch_delivery() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'seal delivery fault';END $$;CREATE TRIGGER reject_batch_delivery BEFORE INSERT ON outbox_delivery FOR EACH ROW EXECUTE FUNCTION reject_batch_delivery()"); e != nil {
		t.Fatal(e)
	}
	if _, e = ob.SealDueNotificationBatches(ctx, 32); e == nil {
		t.Fatal("missing seal atomic failure")
	}
	var count int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM outbox_event WHERE event_type='notification.media_imported.v1'").Scan(&count); e != nil || count != 0 {
		t.Fatal("partial event on failed sealing", count, e)
	}
	if _, e = pool.Exec(ctx, "CREATE OR REPLACE FUNCTION reject_batch_delivery() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.consumer='notify:"+a.String()+"' THEN RAISE EXCEPTION 'one recipient seal fault'; END IF;RETURN NEW;END $$"); e != nil {
		t.Fatal(e)
	}
	if n, e := ob.SealDueNotificationBatches(ctx, 32); e == nil || n != 1 {
		t.Fatal("one bad batch starved independent recipient", n, e)
	}
	var aState, bState string
	if e = pool.QueryRow(ctx, "SELECT state FROM notification_batch WHERE channel_id=$1", a).Scan(&aState); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, "SELECT state FROM notification_batch WHERE channel_id=$1", b).Scan(&bState); e != nil || aState != "ready" || bState != "sealed" {
		t.Fatal("fault isolation lost evidence", aState, bState, e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER reject_batch_delivery ON outbox_delivery"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := outbox.New(pool, idgen.New(), clock.New()).SealDueNotificationBatches(ctx, 32)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM outbox_event WHERE event_type='notification.media_imported.v1'").Scan(&count); e != nil || count != 2 {
		t.Fatal("concurrent seal duplicated event", count, e)
	}
	var eventID uuid.UUID
	if e = pool.QueryRow(ctx, "SELECT event_id FROM notification_batch WHERE channel_id=$1", a).Scan(&eventID); e != nil {
		t.Fatal(e)
	}
	event, e := q.GetOutboxEventByID(ctx, eventID)
	if e != nil || bytes.Contains(event.Payload, []byte("private-notify-token")) {
		t.Fatal("raw file payload leaked", e)
	}
	var payload ports.NotificationBatchPayload
	if json.Unmarshal(event.Payload, &payload) != nil || len(payload.Members) != 2 || payload.MediaID != m.ID {
		t.Fatal("wrong frozen membership", string(event.Payload))
	}
	old, e := q.GetOutboxEventByID(ctx, first.ID)
	if e != nil || !bytes.Equal(old.Payload, first.Payload) {
		t.Fatal("source event rewritten", e)
	}
	old, e = q.GetOutboxEventByID(ctx, second.ID)
	if e != nil || !bytes.Equal(old.Payload, second.Payload) {
		t.Fatal("second source rewritten", e)
	}
	if _, e = pool.Exec(ctx, "UPDATE notification_batch_member SET unit_id=$1", u.ID); e == nil {
		t.Fatal("mutable sealed membership")
	}
	if _, e = pool.Exec(ctx, "UPDATE notification_batch SET deadline=deadline+interval '1 second'"); e == nil {
		t.Fatal("sliding/frozen deadline mutable")
	}
	if _, e = pool.Exec(ctx, "UPDATE outbox_event SET payload='{}'::jsonb WHERE id=$1", event.ID); e == nil {
		t.Fatal("frozen notification body mutable")
	}
	if _, e = pool.Exec(ctx, "UPDATE outbox_event SET payload='{}'::jsonb WHERE id=$1", first.ID); e == nil {
		t.Fatal("source imported evidence mutable")
	}
	for version := int64(postgres.CurrentMigrationVersion); version > 31; version-- {
		if v, e := postgres.Rollback(ctx, dsn, migrationsDir); e != nil || v != version-1 {
			t.Fatal("empty skip migration down", v, e)
		}
	}
	if _, e = postgres.Rollback(ctx, dsn, migrationsDir); e == nil {
		t.Fatal("batch history dropped by downgrade")
	}
	if _, e = postgres.Migrate(ctx, dsn, migrationsDir); e != nil {
		t.Fatal("restore after guard probe", e)
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	legacy, e := ob.Publish(ctx, q.WithTx(tx), "media", u.ID.String(), "media.imported", map[string]string{"fixture": "historical"}, []string{"notify:" + a.String()})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE outbox_delivery SET state='dead' WHERE event_id=$1", legacy.ID); e != nil {
		t.Fatal(e)
	}
	if d, e := ob.Claim(ctx, "notify:"+a.String(), "a", time.Minute); e != nil || d != nil {
		t.Fatal("new batch bypassed historical dead", d, e)
	}
	if d, e := ob.Claim(ctx, "notify:"+b.String(), "b", time.Minute); e != nil || d == nil {
		t.Fatal("foreign recipient blocked by old dead", d, e)
	}
	// Capacity is independent of the production 3-second window. Seed two new
	// fixed-deadline fixture buckets; never mutate an already frozen deadline.
	for _, channel := range []uuid.UUID{a, b} {
		if _, e = pool.Exec(ctx, "INSERT INTO notification_batch(id,media_id,channel_id,deadline) VALUES($1,$2,$3,clock_timestamp()+interval '1 hour')", uuid.New(), m.ID, channel); e != nil {
			t.Fatal(e)
		}
	}
	for range 32 {
		if _, e = publish(files[0]); e != nil {
			t.Fatal(e)
		}
	}
	var max int
	if e = pool.QueryRow(ctx, "SELECT max(member_count) FROM notification_batch").Scan(&max); e != nil || max != 32 {
		t.Fatal("unbounded batch", max, e)
	}
	if n, e := ob.SealDueNotificationBatches(ctx, 32); e != nil || n != 2 {
		t.Fatal("full32 not ready independently", n, e)
	}
	// A 33rd event must get new recipient buckets, never overflow sealed membership.
	if _, e = publish(files[0]); e != nil {
		t.Fatal(e)
	}
	var successors int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM notification_batch WHERE media_id=$1 AND state='open' AND member_count=1", m.ID).Scan(&successors); e != nil || successors != 2 {
		t.Fatal("capacity rollover lost recipient", successors, e)
	}
}
