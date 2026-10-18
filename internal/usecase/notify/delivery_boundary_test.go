package notify_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

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
)

// Explicit test codec uses the same master-key cipher as deployed adapters.
type deliveryCodec struct {
	*crypto.SecretProtector
	key []byte
}

func (c deliveryCodec) DecryptSecret(cipher []byte) ([]byte, error) {
	return crypto.Decrypt(c.key, cipher)
}

type captureChannelRepository struct {
	hash  string
	write ports.NotificationChannelWrite
}

func (r *captureChannelRepository) CreateNotificationChannel(_ context.Context, _ *ports.Actor, w ports.NotificationChannelWrite, _, hash string) (*ports.NotificationChannelObservation, error) {
	r.hash, r.write = hash, w
	return &ports.NotificationChannelObservation{}, nil
}
func (*captureChannelRepository) ListNotificationChannels(context.Context, *ports.Actor) ([]ports.NotificationChannelObservation, error) {
	return nil, nil
}

func TestInjectedCodecPreservesChannelIntentAndCiphertext(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	codec := deliveryCodec{crypto.NewSecretProtector(key), key}
	repo := &captureChannelRepository{}
	dispatcher := notify.NewDispatcher(nil, nil, nil, nil, repo)
	dispatcher.SetClientFactory(codec, nil)
	cfg := notify.ChannelConfig{URL: "https://example.test/hook", Secret: "fixture-secret"}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	if _, err := dispatcher.CreateChannel(context.Background(), actor, "fixture", "webhook", cfg, "once"); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(map[string]any{"name": "fixture", "type": "webhook", "config": cfg})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("notification.channel.create.intent.v1\x00"))
	_, _ = mac.Write(canonical)
	if repo.hash != "hmac.v1:"+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("channel create intent format changed")
	}
	plain, err := crypto.Decrypt(key, repo.write.Ciphertext)
	var decoded notify.ChannelConfig
	if err != nil || json.Unmarshal(plain, &decoded) != nil || decoded != cfg {
		t.Fatal("existing master-key decryptor rejected channel ciphertext", err)
	}
}

func TestReplacementBeforeSendPreservesOldDeliveryPG(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, migrationsDir); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := postgres.Queries(pool)
	gen := idgen.New()
	key := bytes.Repeat([]byte{8}, 32)
	codec := deliveryCodec{crypto.NewSecretProtector(key), key}
	repo := postgres.NewNotificationChannelStore(pool, gen)
	ob := outbox.New(pool, gen, clock.New())
	dispatcher := notify.NewDispatcher(postgres.NewNotificationChannelReader(pool), gen, clock.New(), ob, repo)
	user, err := q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "replacement-boundary", PasswordHash: "fixture", Role: "admin", IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.ID.String(), Role: ports.RoleAdmin}
	dispatcher.SetClientFactory(codec, nil)
	channel, err := dispatcher.CreateChannel(ctx, actor, "old", "webhook", notify.ChannelConfig{URL: "https://example.test/old", Secret: "old-secret"}, "create")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	consumer := "notify:" + channel.ID.String()
	event, err := ob.Publish(ctx, q.WithTx(tx), "media", "fixture", "media.imported", map[string]string{"title": "fixture"}, []string{consumer})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err := ob.Claim(ctx, consumer, "worker", time.Minute)
	if err != nil || claimed == nil {
		t.Fatal("claim", err)
	}
	var replacement *ports.ChannelReplacementReceipt
	sends := 0
	dispatcher.SetClientFactory(codec, func(_ string, _ notify.ChannelConfig) (ports.Notifier, error) {
		replacement, err = notify.NewChannelReplacementService(postgres.NewChannelReplacementStore(pool, gen), codec).Replace(ctx, actor, channel.ID, ports.ChannelReplacementCommand{ExpectedUpdatedAt: channel.UpdatedAt, Name: "new", ChannelType: "webhook", Config: notify.ChannelConfig{URL: "https://example.test/new", Secret: "new-secret"}, AcceptOldPending: true}, "replace")
		if err != nil {
			return nil, err
		}
		return &countingNotifier{send: func() { sends++ }}, nil
	})
	if err = dispatcher.DispatchDelivery(ctx, claimed.ID, channel.ID, "worker", claimed.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if sends != 0 || replacement == nil || replacement.NewChannelID == channel.ID {
		t.Fatal("replacement sent old message or reused channel identity")
	}
	old, err := q.GetNotificationChannelByID(ctx, channel.ID)
	if err != nil || old.IsEnabled {
		t.Fatal("old channel remains enabled", err)
	}
	row, err := q.GetOutboxDeliveryByID(ctx, claimed.ID)
	if err != nil || row.EventID != event.ID || row.Consumer != consumer || row.State != "pending" || row.Attempts != 0 || row.LeaseOwner.Valid {
		t.Fatal("old delivery identity/budget changed", row, err)
	}
	next, err := ob.Claim(ctx, "notify:"+replacement.NewChannelID.String(), "new", time.Minute)
	if err != nil || next != nil {
		t.Fatal("old message migrated to new channel", err)
	}
}

type countingNotifier struct{ send func() }

func (c *countingNotifier) Send(context.Context, ports.Notification) error { c.send(); return nil }
func (c *countingNotifier) Test(context.Context) error                     { return nil }
