package postgres_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

type pushFixture struct {
	endpointConfigFixture
	history string
	service *notify.WebPushService
	repo    *postgres.WebPushStore
	config  ports.WebPushPublicConfig
	sub     ports.WebPushSubscription
}

func newPushFixture(t *testing.T) pushFixture {
	t.Helper()
	f := newEndpointConfigFixture(t)
	history := repositoryMigrationsThrough(t, 120)
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), history); err != nil || version != 120 {
		t.Fatal(version, err)
	}
	f.exec(t, "UPDATE app_user SET role='member' WHERE id=$1", f.actor.ID)
	f.actor.Role = ports.RoleMember
	gen := idgen.New()
	repo := postgres.NewWebPushStore(f.p, gen, outbox.New(f.p, gen, clock.New()))
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
	service := notify.NewWebPushService(repo, f.secrets, public, true)
	config, err := service.Configuration(t.Context(), f.actor)
	if err != nil {
		t.Fatal(err)
	}
	return pushFixture{f, history, service, repo, *config, ports.WebPushSubscription{Endpoint: "https://fcm.googleapis.com/owned-device", P256DH: public, Auth: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))}}
}
func (f pushFixture) register(t *testing.T, key string) *ports.WebPushDevice {
	t.Helper()
	r, err := f.service.Register(t.Context(), f.actor, uuid.New(), f.config.Version, f.sub, key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f pushFixture) countsAll(t *testing.T) []int {
	t.Helper()
	var device, audit, event, receipt, delivery int
	if err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM web_push_device),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record),(SELECT count(*) FROM outbox_delivery)`).Scan(&device, &audit, &event, &receipt, &delivery); err != nil {
		t.Fatal(err)
	}
	return []int{device, audit, event, receipt, delivery}
}
func TestWebPushDeviceEncryptedReceiptAndIsolationPG(t *testing.T) {
	f := newPushFixture(t)
	r := f.register(t, "register")
	if got := f.countsAll(t); !reflect.DeepEqual(got, []int{1, 1, 1, 1, 0}) {
		t.Fatal(got)
	}
	var cipher []byte
	var user uuid.UUID
	var version string
	if err := f.p.QueryRow(t.Context(), `SELECT user_id,vapid_version,subscription_encrypted FROM web_push_device WHERE id=$1`, r.ID).Scan(&user, &version, &cipher); err != nil {
		t.Fatal(err)
	}
	if user.String() != f.actor.ID || version != f.config.Version || bytes.Contains(cipher, []byte(f.sub.Endpoint)) {
		t.Fatal("invalid sealed binding")
	}
	plain, err := f.secrets.DecryptSecret(cipher)
	var stored ports.WebPushSubscription
	if err != nil || json.Unmarshal(plain, &stored) != nil || stored != f.sub {
		t.Fatal("wrong encrypted subscription", err)
	}
	var safe string
	if err = f.p.QueryRow(t.Context(), `SELECT string_agg(response_body::text,'') FROM idempotency_record`).Scan(&safe); err != nil || strings.Contains(safe, f.sub.Endpoint) || strings.Contains(safe, f.sub.P256DH) || strings.Contains(safe, f.sub.Auth) {
		t.Fatal("receipt capability leak", err)
	}
	foreign := pgtest.CreateUserActor(t, db.New(f.p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	if rows, err := f.service.List(t.Context(), foreign); err != nil || len(rows) != 0 {
		t.Fatal("foreign list", err)
	}
	if _, err = f.service.Receipt(t.Context(), foreign, "register"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("foreign receipt", err)
	}
	if _, err = f.service.Revoke(t.Context(), foreign, r.ID, r.UpdatedAt, "foreign"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("foreign revoke", err)
	}
	if _, err = f.service.Register(t.Context(), foreign, uuid.New(), f.config.Version, f.sub, "hijack"); !errors.Is(err, ports.ErrWebPushConflict) {
		t.Fatal("endpoint reassigned", err)
	}
	disabled := notify.NewWebPushService(f.repo, f.secrets, "", false)
	replay, err := disabled.Register(t.Context(), f.actor, r.ID, f.config.Version, f.sub, "register")
	if err != nil || !sameHistoricalJSON(r, replay) {
		t.Fatal("offline disabled replay", err)
	}
	revoked, err := disabled.Revoke(t.Context(), f.actor, r.ID, r.UpdatedAt, "revoke")
	if err != nil || revoked.Status != "revoked" {
		t.Fatal(err)
	}
	replay, err = disabled.Receipt(t.Context(), f.actor, "register")
	if err != nil || replay.Status != "active" {
		t.Fatal("historical receipt changed", err)
	}
	if _, err = f.service.Register(t.Context(), f.actor, uuid.New(), f.config.Version, f.sub, "reuse"); !errors.Is(err, ports.ErrWebPushConflict) {
		t.Fatal("revoked endpoint rebound", err)
	}
}
func TestWebPushDeviceConcurrentAndQuotaPG(t *testing.T) {
	f := newPushFixture(t)
	id := uuid.New()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := f.service.Register(t.Context(), f.actor, id, f.config.Version, f.sub, "concurrent")
			results <- err
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := f.countsAll(t); !reflect.DeepEqual(got, []int{1, 1, 1, 1, 0}) {
		t.Fatal(got)
	}
	for i := 1; i < 10; i++ {
		f.sub.Endpoint = fmt.Sprintf("https://fcm.googleapis.com/device-%d", i)
		f.register(t, fmt.Sprintf("quota-%d", i))
	}
	before := f.countsAll(t)
	f.sub.Endpoint = "https://fcm.googleapis.com/over-quota"
	if _, err := f.service.Register(t.Context(), f.actor, uuid.New(), f.config.Version, f.sub, "excess"); !errors.Is(err, ports.ErrWebPushConflict) {
		t.Fatal("quota bypass", err)
	}
	if !reflect.DeepEqual(before, f.countsAll(t)) {
		t.Fatal("quota failure changed ledger")
	}
}
func TestWebPushDevicePermissionAndCancelledLockPG(t *testing.T) {
	f := newPushFixture(t)
	r := f.register(t, "old")
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", f.actor.ID)
	if _, err := f.service.Receipt(t.Context(), f.actor, "old"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked receipt", err)
	}
	if _, err := f.service.List(t.Context(), f.actor); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked list", err)
	}
	f.exec(t, "UPDATE app_user SET is_active=true WHERE id=$1", f.actor.ID)
	lock, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	if _, err = lock.Exec(t.Context(), "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", f.actor.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err = f.service.Revoke(ctx, f.actor, r.ID, r.UpdatedAt, "cancelled"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("wait ignored cancellation", err)
	}
	if err = lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.countsAll(t), []int{1, 1, 1, 1, 0}) {
		t.Fatal("cancelled transaction wrote")
	}
}
func TestWebPushDeviceTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newPushFixture(t)
			f.exec(t, `CREATE FUNCTION push_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private push endpoint'; END $$`)
			f.exec(t, "CREATE TRIGGER push_tail_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION push_tail_fault()")
			id := uuid.New()
			if _, err := f.service.Register(t.Context(), f.actor, id, f.config.Version, f.sub, "tail"); !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe failure", err)
			}
			if got := f.countsAll(t); !reflect.DeepEqual(got, []int{0, 0, 0, 0, 0}) {
				t.Fatal("partial commit", got)
			}
			f.exec(t, "DROP TRIGGER push_tail_fault ON "+table)
			if _, err := f.service.Register(t.Context(), f.actor, id, f.config.Version, f.sub, "tail"); err != nil {
				t.Fatal("retry failed", err)
			}
		})
	}
}
func TestWebPushDeviceVersionAndImmutableHistoryPG(t *testing.T) {
	f := newPushFixture(t)
	if _, err := f.service.Register(t.Context(), f.actor, uuid.New(), strings.Repeat("a", 64), f.sub, "stale"); !errors.Is(err, ports.ErrWebPushConflict) {
		t.Fatal(err)
	}
	if version, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history); err != nil || version != 119 {
		t.Fatal(version, err)
	}
	if _, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), f.history); err != nil {
		t.Fatal(err)
	}
	r := f.register(t, "new")
	for _, query := range []string{`UPDATE web_push_device SET user_id=gen_random_uuid() WHERE id=$1`, `UPDATE web_push_device SET subscription_encrypted=repeat('a',60)::bytea WHERE id=$1`, `DELETE FROM web_push_device WHERE id=$1`} {
		if _, err := f.p.Exec(t.Context(), query, r.ID); err == nil {
			t.Fatal("binding mutable")
		}
	}
	if _, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history); err == nil {
		t.Fatal("historical downgrade allowed")
	}
	if _, err := f.service.Revoke(t.Context(), f.actor, r.ID, r.UpdatedAt.Add(time.Microsecond), "stale-revoke"); !errors.Is(err, ports.ErrWebPushConflict) {
		t.Fatal(err)
	}
	if _, err := f.service.Revoke(t.Context(), f.actor, r.ID, r.UpdatedAt, "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(t.Context(), `UPDATE web_push_device SET status='active',updated_at=clock_timestamp() WHERE id=$1`, r.ID); err == nil {
		t.Fatal("revoked reenabled")
	}
}
