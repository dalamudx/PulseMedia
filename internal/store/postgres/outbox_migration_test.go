package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestOutboxMigrationQuarantinesPlaintextAndUnspecifiedRecipients(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	dir := t.TempDir()
	paths, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if filepath.Base(path) >= "00018" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(path)), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := postgres.Migrate(ctx, dsn, dir); err != nil || v != 17 {
		t.Fatal(v, err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ch, event := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO notification_channel(id,name,channel_type,config) VALUES($1,'Old','webhook','{"url":"https://private.example","secret":"private-legacy-secret"}')`, ch); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) VALUES($1,'media','fixture','media.imported','{}',1)`, event); err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, consumer := range []string{"notify", "mediaserver.refresh", "notify:" + ch.String()} {
		state := "pending"
		if i == 2 {
			state = "delivered"
		}
		if _, err := pool.Exec(ctx, "INSERT INTO outbox_delivery(id,event_id,consumer,aggregate_type,aggregate_id,seq,state) VALUES($1,$2,$3,'media','fixture',1,$4)", ids[i], event, consumer, state); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, err)
	}
	q := db.New(pool)
	channel, err := q.GetNotificationChannelByID(ctx, ch)
	if err != nil || channel.IsEnabled || string(channel.Config) != "{}" || len(channel.ConfigEncrypted) != 0 {
		t.Fatal("legacy plaintext falsely considered usable", err)
	}
	for i, id := range ids {
		d, err := q.GetOutboxDeliveryByID(ctx, id)
		want := "dead"
		if i == 2 {
			want = "delivered"
		}
		if err != nil || d.State != want {
			t.Fatalf("legacy delivery evidence fabricated: %s %v", d.State, err)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE notification_channel SET config=$2 WHERE id=$1", ch, []byte(`{"secret":"cannot-reintroduce-plaintext"}`)); err == nil {
		t.Fatal("plaintext constraint absent")
	}
	// Remove schema21/20/19, then test schema18 down independently.
	for expected := int64(postgres.CurrentMigrationVersion - 1); expected >= 18; expected-- {
		if v, err := postgres.Rollback(ctx, dsn, "../../../migrations"); err != nil || v != expected {
			t.Fatal(v, err)
		}
	}
	// Down is intentionally non-restorative: removed secrets never resurrect.
	if v, err := postgres.Rollback(ctx, dsn, "../../../migrations"); err != nil || v != 17 {
		t.Fatal(err)
	}
	var cfg string
	var enabled bool
	if err := pool.QueryRow(ctx, "SELECT config::text,is_enabled FROM notification_channel WHERE id=$1", ch).Scan(&cfg, &enabled); err != nil || cfg != "{}" || enabled {
		t.Fatal("downgrade restored credentials", err)
	}
}
