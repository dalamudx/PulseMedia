package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	cloud "github.com/dalamudx/PulseMedia/internal/usecase/cookiecloud"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type cookieConfigFixture struct {
	p            *pgxpool.Pool
	store        *postgres.CookieCloudConfigStore
	service      *cloud.ConfigService
	secret       *crypto.SecretProtector
	owner, other *ports.Actor
	dsn, history string
}

func newCookieConfigFixture(t *testing.T) cookieConfigFixture {
	t.Helper()
	f := cookieConfigFixture{dsn: pgtest.GetCluster(t).NewTestDB(t), history: repositoryMigrationsThrough(t, 108)}
	if version, err := postgres.Migrate(t.Context(), f.dsn, f.history); err != nil || version != 108 {
		t.Fatal("config migration", version, err)
	}
	p, err := postgres.NewPool(t.Context(), f.dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	f.p = p
	t.Cleanup(p.Close)
	f.store = postgres.NewCookieCloudConfigStore(p, idgen.New())
	f.secret = crypto.NewSecretProtector(bytes.Repeat([]byte{0x3b}, 32))
	f.service = cloud.NewConfigService(f.store, f.secret)
	f.owner, f.other = f.user(t, "owner"), f.user(t, "other")
	return f
}
func (f cookieConfigFixture) user(t *testing.T, name string) *ports.Actor {
	t.Helper()
	a := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	if _, err := db.New(f.p).CreateUser(t.Context(), db.CreateUserParams{ID: uuid.MustParse(a.ID), Username: name, PasswordHash: "owned-fixture", Role: ports.RoleAdmin, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	return a
}
func cookieConfigInput() ports.CookieCloudConfigInput {
	return ports.CookieCloudConfigInput{Enabled: true, IntervalSeconds: 1200, Connection: &ports.CookieCloudConnection{ServerURL: "https://cloud.example/private-prefix", UserKey: "owned-sync-key", Password: "owned-password"}}
}
func (f cookieConfigFixture) save(t *testing.T, in ports.CookieCloudConfigInput, key string) *ports.CookieCloudConfigReceipt {
	t.Helper()
	r, err := f.service.Save(t.Context(), f.owner, in, key)
	if err != nil || r == nil {
		t.Fatal("config save", err)
	}
	return r
}
func (f cookieConfigFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.p.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f cookieConfigFixture) count(t *testing.T, table string) int64 {
	t.Helper()
	var n int64
	if err := f.p.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f cookieConfigFixture) cipher(t *testing.T) []byte {
	t.Helper()
	var out []byte
	if err := f.p.QueryRow(t.Context(), "SELECT connection_encrypted FROM cookiecloud_configuration WHERE owner_id=$1", f.owner.ID).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
func (f cookieConfigFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.p.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('app_user','session','api_token') ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		var value string
		if err = f.p.QueryRow(t.Context(), "SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]'::jsonb)::text FROM "+pgx.Identifier{name}.Sanitize()+" x").Scan(&value); err != nil {
			t.Fatal(err)
		}
		out[name] = value
	}
	return out
}

func TestCookieCloudConfigConcurrentOriginalPG(t *testing.T) {
	f := newCookieConfigFixture(t)
	in := cookieConfigInput()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]*ports.CookieCloudConfigReceipt, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.service.Save(t.Context(), f.owner, in, "original")
		}(i)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(results[0], results[1]) {
		t.Fatal("same-key concurrency did not restore one result", errs)
	}
	for _, table := range []string{"cookiecloud_configuration", "audit_log", "outbox_event", "idempotency_record"} {
		if f.count(t, table) != 1 {
			t.Fatal("duplicate configuration transaction", table)
		}
	}
	cipher := f.cipher(t)
	if bytes.Contains(cipher, []byte(in.Connection.Password)) || bytes.Contains(cipher, []byte(in.Connection.UserKey)) {
		t.Fatal("plaintext connection persisted")
	}
	plain, err := f.secret.DecryptSecret(cipher)
	if err != nil {
		t.Fatal("saved envelope cannot be decrypted", err)
	}
	defer clear(plain)
	var connection ports.CookieCloudConnection
	if json.Unmarshal(plain, &connection) != nil || connection != *in.Connection {
		t.Fatal("encrypted connection lost input")
	}
	wire, _ := json.Marshal(results[0])
	for _, secret := range []string{in.Connection.Password, in.Connection.UserKey, "private-prefix"} {
		if bytes.Contains(wire, []byte(secret)) {
			t.Fatal("receipt exposed a connection secret")
		}
	}
	before := f.snapshot(t)
	restored, err := f.service.Receipt(t.Context(), f.owner, "original")
	if err != nil || !reflect.DeepEqual(restored, results[0]) || !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("read-only receipt changed complete non-auth rows", err)
	}
}

func TestCookieCloudConfigAccountHistoricalAndOfflinePG(t *testing.T) {
	f := newCookieConfigFixture(t)
	in := cookieConfigInput()
	original := f.save(t, in, "saved")
	cipher := f.cipher(t)
	disabled := ports.CookieCloudConfigInput{ExpectedVersion: 1, Enabled: false, IntervalSeconds: 1200}
	f.save(t, disabled, "disable-offline")
	if !bytes.Equal(cipher, f.cipher(t)) {
		t.Fatal("offline deactivation changed original ciphertext")
	}
	restored, err := f.service.Save(t.Context(), f.owner, in, "saved")
	if err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatal("historical receipt did not survive current config change", err)
	}
	in.Enabled = false
	if _, err = f.service.Save(t.Context(), f.owner, in, "saved"); !errors.Is(err, ports.ErrCookieCloudConfigConflict) {
		t.Fatal("same key changed input accepted", err)
	}
	if _, err = f.service.Receipt(t.Context(), f.other, "saved"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("foreign command key escaped scope", err)
	}
	other, err := f.service.Save(t.Context(), f.other, cookieConfigInput(), "saved")
	if err != nil || other.Config.OwnerID.String() != f.other.ID || f.count(t, "cookiecloud_configuration") != 2 {
		t.Fatal("account configuration identities collided", err)
	}
	f.exec(t, "UPDATE app_user SET role='member' WHERE id=$1", f.owner.ID)
	if _, err = f.service.Receipt(t.Context(), f.owner, "saved"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked administrator restored a private receipt", err)
	}
	if _, err = f.service.Read(t.Context(), f.owner); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked administrator read configuration", err)
	}
}

func TestCookieCloudConfigConcurrentVersionsPG(t *testing.T) {
	f := newCookieConfigFixture(t)
	f.save(t, cookieConfigInput(), "initial")
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.service.Save(t.Context(), f.owner, ports.CookieCloudConfigInput{ExpectedVersion: 1, Enabled: false, IntervalSeconds: 1200}, fmt.Sprintf("edit-%d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	passed, conflicted := 0, 0
	for _, err := range errs {
		if err == nil {
			passed++
		} else if errors.Is(err, ports.ErrCookieCloudConfigConflict) {
			conflicted++
		} else {
			t.Fatal("unexpected concurrent result", err)
		}
	}
	if passed != 1 || conflicted != 1 || f.count(t, "audit_log") != 2 || f.count(t, "idempotency_record") != 2 {
		t.Fatal("version conflict left duplicate results")
	}
}

func TestCookieCloudConfigTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newCookieConfigFixture(t)
			before := f.snapshot(t)
			f.exec(t, `CREATE FUNCTION cookie_config_test_fail() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned tail failure'; END $$`)
			f.exec(t, "CREATE CONSTRAINT TRIGGER cookie_config_test_tail AFTER INSERT ON "+pgx.Identifier{table}.Sanitize()+" DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION cookie_config_test_fail()")
			if _, err := f.service.Save(t.Context(), f.owner, cookieConfigInput(), "retry"); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("tail failure classification", err)
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("tail failure changed complete non-auth rows")
			}
			f.exec(t, "DROP TRIGGER cookie_config_test_tail ON "+pgx.Identifier{table}.Sanitize())
			f.exec(t, "DROP FUNCTION cookie_config_test_fail()")
			if got := f.save(t, cookieConfigInput(), "retry"); got.Config.Version != 1 {
				t.Fatal("rollback did not preserve original retry")
			}
		})
	}
}

func TestCookieCloudConfigDeferredRevocationPG(t *testing.T) {
	f := newCookieConfigFixture(t)
	before := f.snapshot(t)
	f.exec(t, `CREATE FUNCTION cookie_config_test_revoke() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='cookiecloud.configuration' THEN UPDATE app_user SET role='member' WHERE id=NEW.actor_id::uuid; END IF; RETURN NEW; END $$`)
	f.exec(t, `CREATE CONSTRAINT TRIGGER cookie_config_test_revoke AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION cookie_config_test_revoke()`)
	if _, err := f.service.Save(t.Context(), f.owner, cookieConfigInput(), "revoke"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("late permission change committed", err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("late revocation changed non-auth rows")
	}
	var role string
	if err := f.p.QueryRow(t.Context(), "SELECT role FROM app_user WHERE id=$1", f.owner.ID).Scan(&role); err != nil || role != ports.RoleAdmin {
		t.Fatal("revocation did not roll back with configuration", err)
	}
	f.exec(t, "DROP TRIGGER cookie_config_test_revoke ON audit_log")
	f.exec(t, "DROP FUNCTION cookie_config_test_revoke()")
	f.save(t, cookieConfigInput(), "revoke")
}

func TestCookieCloudConfigSchema108RoundtripPG(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newCookieConfigFixture(t)
		if version, err := postgres.Rollback(t.Context(), f.dsn, f.history); err != nil || version != 107 {
			t.Fatal("empty historical down", version, err)
		}
		if version, err := postgres.Migrate(t.Context(), f.dsn, f.history); err != nil || version != 108 {
			t.Fatal("historical reinstall", version, err)
		}
	})
	t.Run("history", func(t *testing.T) {
		f := newCookieConfigFixture(t)
		f.save(t, cookieConfigInput(), "history")
		before := f.snapshot(t)
		for _, statement := range []string{"DELETE FROM cookiecloud_configuration", "TRUNCATE cookiecloud_configuration", "UPDATE cookiecloud_configuration SET version=version+2"} {
			if _, err := f.p.Exec(t.Context(), statement); err == nil {
				t.Fatal("configuration history bypass accepted")
			}
		}
		if _, err := postgres.Rollback(t.Context(), f.dsn, f.history); err == nil {
			t.Fatal("configuration history downgraded")
		}
		if !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("failed history mutations changed rows")
		}
	})
}

func TestCookieCloudConfigNoExecutionAndCancellationPG(t *testing.T) {
	f := newCookieConfigFixture(t)
	before := f.snapshot(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.service.Save(ctx, f.owner, cookieConfigInput(), "cancelled"); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("cancelled save changed rows", err)
	}
	f.save(t, cookieConfigInput(), "config-only")
	after := f.snapshot(t)
	allowed := map[string]bool{"cookiecloud_configuration": true, "audit_log": true, "outbox_event": true, "idempotency_record": true}
	for table, value := range before {
		if !allowed[table] && after[table] != value {
			t.Fatal("saving configuration mutated unrelated rows", table)
		}
	}
	if _, err := f.service.Read(t.Context(), f.owner); err != nil || !reflect.DeepEqual(after, f.snapshot(t)) {
		t.Fatal("configuration GET changed non-auth data", err)
	}
	for _, sensitive := range []string{"owned-password", "owned-sync-key", "private-prefix"} {
		for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
			if strings.Contains(after[table], sensitive) {
				t.Fatal("success ledger exposed a connection secret", table)
			}
		}
	}
}
