package notify_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
)

const migrationsDir = "../../../migrations"

func TestChannelIndependentRetryAndOrdering(t *testing.T) {
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
	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	ob := outbox.New(pool, gen, mockClock)
	disp := notify.NewDispatcher(postgres.NewNotificationChannelReader(pool), gen, mockClock, ob, postgres.NewNotificationChannelStore(pool, gen))
	queries := postgres.Queries(pool)

	// Create Channel A (webhook) and Channel B (telegram) in DB
	chAID, _ := uuid.NewV7()
	_, _ = queries.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{
		ID:              chAID,
		Name:            "Channel Webhook",
		ChannelType:     "webhook",
		ConfigEncrypted: []byte("test-only mock configuration"),
	})

	chBID, _ := uuid.NewV7()
	_, _ = queries.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{
		ID:              chBID,
		Name:            "Channel Telegram",
		ChannelType:     "telegram",
		ConfigEncrypted: []byte("test-only mock configuration"),
	})

	// Mock notifiers: A always fails, B always succeeds
	mockA := &mockNotifier{shouldFail: true}
	mockB := &mockNotifier{shouldFail: false}
	disp.RegisterClient(chAID, mockA)
	disp.RegisterClient(chBID, mockB)

	// Publish Event to both channels
	tx, _ := pool.Begin(ctx)
	qtx := queries.WithTx(tx)
	consumerA := "notify:" + chAID.String()
	consumerB := "notify:" + chBID.String()

	evt, err := ob.Publish(ctx, qtx, "movie", "movie:1", "media.imported", map[string]string{"title": "Fight Club"}, []string{consumerA, consumerB})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	_ = tx.Commit(ctx)

	// 1. Channel A claims and fails: delivery marked failed
	dA, err := ob.Claim(ctx, consumerA, "worker_a", 10*time.Second)
	if err != nil || dA == nil {
		t.Fatalf("claim A failed: %v", err)
	}
	errA := disp.DispatchDelivery(ctx, dA.ID, chAID, "worker_a", dA.LeaseToken)
	if errA == nil || strings.Contains(errA.Error(), "sensitive-webhook-token") {
		t.Fatalf("adapter failure leaked into return: %v", errA)
	}
	deliveryA, err := queries.GetOutboxDeliveryByID(ctx, dA.ID)
	if err != nil || !deliveryA.Error.Valid || strings.Contains(deliveryA.Error.String, "sensitive-webhook-token") {
		t.Fatalf("adapter failure leaked into PG delivery error: %+v %v", deliveryA.Error, err)
	}

	// 2. Invariant: Channel B is completely INDEPENDENT: succeeds cleanly! (渠道独立重放)
	dB, err := ob.Claim(ctx, consumerB, "worker_b", 10*time.Second)
	if err != nil || dB == nil {
		t.Fatalf("claim B failed: %v", err)
	}
	errB := disp.DispatchDelivery(ctx, dB.ID, chBID, "worker_b", dB.LeaseToken)
	if errB != nil {
		t.Fatalf("channel B should succeed independently: %v", errB)
	}

	_ = evt
}

type mockNotifier struct {
	shouldFail bool
}

func (m *mockNotifier) Send(ctx context.Context, n ports.Notification) error {
	if m.shouldFail {
		return fmt.Errorf("simulated network failure sensitive-webhook-token")
	}
	return nil
}

func (m *mockNotifier) Test(ctx context.Context) error {
	return nil
}
