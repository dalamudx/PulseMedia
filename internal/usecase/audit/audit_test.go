package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/audit"
)

const migrationsDir = "../../../migrations"

func TestAuditLogPersistence(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	defer pool.Close()

	gen := idgen.New()
	mockClock := clock.NewMock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	svc := audit.NewService(postgres.NewLegacyAuditWriter(pool), gen, mockClock)

	actor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   "admin-uuid-123",
		Name: "admin",
		Role: ports.RoleAdmin,
	}

	// 1. Record critical action
	entry, err := svc.Record(ctx, actor, "subscription.create", "media", "media-123", map[string]any{
		"title": "Breaking Bad",
	})
	if err != nil || entry == nil {
		t.Fatalf("record audit failed: %v", err)
	}

	if entry.Action != "subscription.create" || entry.ActorID != "admin-uuid-123" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	var actorType, actorID, action, targetType, targetID, payload string
	var created time.Time
	if err := pool.QueryRow(ctx, `SELECT actor_type, actor_id, action, target_type, target_id, payload::text, created_at FROM audit_log WHERE id=$1`, entry.ID).Scan(&actorType, &actorID, &action, &targetType, &targetID, &payload, &created); err != nil {
		t.Fatal(err)
	}
	if actorType != "user" || actorID != entry.ActorID || action != entry.Action || targetType != "media" || targetID != "media-123" || payload != `{"title": "Breaking Bad"}` || !created.Equal(mockClock.Now()) {
		t.Fatalf("stored audit differs: %s %s %s %s %s %s %v", actorType, actorID, action, targetType, targetID, payload, created)
	}
	for _, details := range []map[string]any{nil, {"invalid": make(chan int)}} {
		e, err := svc.Record(ctx, nil, "legacy.test", "system", "system", details)
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT actor_type, actor_id, payload::text FROM audit_log WHERE id=$1`, e.ID).Scan(&actorType, &actorID, &payload); err != nil {
			t.Fatal(err)
		}
		if actorType != "system" || actorID != "system" || payload != "{}" {
			t.Fatalf("legacy fallback differs: %s %s %s", actorType, actorID, payload)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e, err := svc.Record(cancelled, nil, "cancelled", "system", "system", nil); e != nil || err != context.Canceled {
		t.Fatalf("cancelled audit: %v %v", e, err)
	}
	pool.Close()
	if _, err := svc.Record(ctx, nil, "secret-action", "system", "system", nil); err != ports.ErrUnavailable {
		t.Fatalf("storage error class: %v", err)
	}
}
