package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type checkinFixture struct {
	p       *pgxpool.Pool
	s       *postgres.SiteCheckinConfigStore
	a       *ports.Actor
	id      uuid.UUID
	dsn     string
	secrets *checkinSecrets
}

func newCheckinFixture(t *testing.T) *checkinFixture {
	return newCheckinFixtureAt(t, "../../../migrations", postgres.CurrentMigrationVersion)
}

func newCheckinFixtureAt(t *testing.T, migrationDir string, expectedVersion int64) *checkinFixture {
	t.Helper()
	ctx := t.Context()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	v, e := postgres.Migrate(ctx, dsn, migrationDir)
	if e != nil || v != expectedVersion {
		t.Fatal(v, e)
	}
	p, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	f := &checkinFixture{p: p, id: uuid.New(), dsn: dsn, secrets: &checkinSecrets{key: bytes.Repeat([]byte{41}, 32)}}
	f.a = f.user(t)
	cookie, e := f.secrets.EncryptSecret([]byte("session=fixture"))
	if e != nil {
		t.Fatal(e)
	}
	root, e := f.secrets.EncryptSecret([]byte("https://nexus.test/root/"))
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'checkin','https://nexus.test',$2)`, f.id, cookie)
	f.exec(t, `INSERT INTO indexer_configuration(indexer_id,protocol,url_encrypted,behavior_version,configuration_version) VALUES($1,'nexusphp',$2,'explicit-v1',1)`, f.id, root)
	f.s = postgres.NewSiteCheckinConfigStore(p, idgen.New(), f.secrets)
	return f
}
func (f *checkinFixture) user(t *testing.T) *ports.Actor {
	t.Helper()
	u, e := db.New(f.p).CreateUser(t.Context(), db.CreateUserParams{ID: uuid.New(), Username: uuid.NewString(), PasswordHash: "fixture", Role: "admin", IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	return &ports.Actor{Type: ports.ActorTypeUser, ID: u.ID.String(), Role: "admin"}
}
func (f *checkinFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, e := f.p.Exec(t.Context(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func (f *checkinFixture) input() ports.SiteCheckinConfigInput {
	return ports.SiteCheckinConfigInput{ExpectedAccountID: "18446744073709551615", Profile: ports.NexusPHP1103CheckinProfile, Enabled: true, Timezone: "Asia/Shanghai", DailyAt: "09:00", ExpectedSourceVersion: 1}
}
func (f *checkinFixture) save(t *testing.T, in ports.SiteCheckinConfigInput, key string) *ports.SiteCheckinConfigReceipt {
	t.Helper()
	r, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, in, key)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func checkinErr(t *testing.T, e, want error) {
	t.Helper()
	if !errors.Is(e, want) {
		t.Fatalf("error=%v want=%v", e, want)
	}
}
func (f *checkinFixture) counts(t *testing.T, want int) {
	t.Helper()
	for _, table := range []string{"site_checkin_config", "site_checkin_config_receipt", "audit_log", "outbox_event"} {
		var n int
		if e := f.p.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != want {
			t.Fatalf("%s: %d want %d err %v", table, n, want, e)
		}
	}
}

func TestSiteCheckinConfigAuthorizationPG(t *testing.T) {
	f := newCheckinFixture(t)
	ctx := t.Context()
	for _, a := range []*ports.Actor{
		nil,
		{Type: ports.ActorTypeUser, ID: f.a.ID, Role: "member"},
		{Type: ports.ActorTypeUser, ID: f.a.ID, Role: "admin", Scopes: []string{}},
		{Type: ports.ActorTypeSystem, ID: f.a.ID, Role: "admin"},
		{Type: ports.ActorTypeUser, ID: "bad", Role: "admin"},
		{Type: ports.ActorTypeUser, ID: "urn:uuid:" + f.a.ID, Role: "admin"},
	} {
		want := ports.ErrForbidden
		if a == nil {
			want = ports.ErrUnauthorized
		}
		_, e := f.s.ReadSiteCheckinConfig(ctx, a, f.id)
		checkinErr(t, e, want)
		_, e = f.s.ReadSiteCheckinConfigReceipt(ctx, a, f.id, "x")
		checkinErr(t, e, want)
		_, e = f.s.SaveSiteCheckinConfig(ctx, a, f.id, f.input(), "x")
		checkinErr(t, e, want)
	}
	if r, e := f.s.ReadSiteCheckinConfig(ctx, f.a, f.id); r != nil || e != nil {
		t.Fatal(r, e)
	}
	_, e := f.s.ReadSiteCheckinConfig(ctx, f.a, uuid.New())
	checkinErr(t, e, ports.ErrNotFound)
	original := f.save(t, f.input(), "first")
	other := f.user(t)
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
	c, e := f.s.ReadSiteCheckinConfig(ctx, other, f.id)
	if e != nil || !reflect.DeepEqual(c, &original.Config) {
		t.Fatal(c, e)
	}
	_, e = f.s.ReadSiteCheckinConfigReceipt(ctx, other, f.id, "first")
	checkinErr(t, e, ports.ErrNotFound)
}
func TestSiteCheckinConfigRevocationAfterWaitPG(t *testing.T) {
	f := newCheckinFixture(t)
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(t.Context(), `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID)); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "wait"); done <- e }()
	// Confirm the repository is blocked on the actual database lock.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var n int
		e = f.p.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no lock wait")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	checkinErr(t, <-done, ports.ErrForbidden)
	f.counts(t, 0)
}
func TestSiteCheckinConfigConcurrencyPG(t *testing.T) {
	f := newCheckinFixture(t)
	var wg sync.WaitGroup
	results := make(chan *ports.SiteCheckinConfigReceipt, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			r, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "same")
			results <- r
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	a, b := <-results, <-results
	if !reflect.DeepEqual(a, b) {
		t.Fatal("replay changed")
	}
	f.counts(t, 1)
	changed := f.input()
	changed.DailyAt = "09:01"
	_, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, changed, "same")
	checkinErr(t, e, ports.ErrEndpointVersionConflict)
	_, e = f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, changed, "new")
	checkinErr(t, e, ports.ErrEndpointVersionConflict)
	f.exec(t, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, f.id)
	f.exec(t, `ALTER TABLE indexer_configuration DISABLE TRIGGER indexer_management_configuration_guard`)
	f.exec(t, `ALTER TABLE indexer_configuration DISABLE TRIGGER indexer_management_configuration_guard`)
	f.exec(t, `UPDATE indexer_configuration SET url_encrypted='broken' WHERE indexer_id=$1`, f.id)
	replay, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "same")
	if e != nil || !reflect.DeepEqual(a, replay) {
		t.Fatal(replay, e)
	}
}
func TestSiteCheckinConfigValidationPG(t *testing.T) {
	f := newCheckinFixture(t)
	f.exec(t, `ALTER TABLE indexer_configuration DISABLE TRIGGER indexer_management_configuration_guard`)
	for _, uid := range []string{"0", "01", "+1", "18446744073709551616", " 1"} {
		in := f.input()
		in.ExpectedAccountID = uid
		_, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, in, "key")
		checkinErr(t, e, ports.ErrRejected)
	}
	for _, key := range []string{"", "bad/key", "空", string(bytes.Repeat([]byte{'x'}, 161))} {
		_, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), key)
		checkinErr(t, e, ports.ErrRejected)
	}
	for _, raw := range []string{"https://nexus.test/?x=1", "https://u:p@nexus.test/", "https://nexus.test/a/../b", "https://nexus.test/%2e", "https://nexus.test/#x", "https://nexus.test:0/"} {
		cipher, e := f.secrets.EncryptSecret([]byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		f.exec(t, `UPDATE indexer_configuration SET url_encrypted=$2 WHERE indexer_id=$1`, f.id, cipher)
		_, e = f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "url")
		want := ports.ErrRejected
		if raw == "https://nexus.test:0/" || raw == "https://nexus.test/#x" {
			want = ports.ErrUnavailable
		}
		checkinErr(t, e, want)
	}
	f.exec(t, `UPDATE indexer_configuration SET protocol='torznab' WHERE indexer_id=$1`, f.id)
	in := f.input()
	in.Enabled = false
	_, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, in, "protocol")
	checkinErr(t, e, ports.ErrRejected)
	f.counts(t, 0)
}
func TestSiteCheckinConfigDisableAndHistoryPG(t *testing.T) {
	f := newCheckinFixture(t)
	original := f.save(t, f.input(), "first")
	f.exec(t, `ALTER TABLE indexer_configuration DISABLE TRIGGER indexer_management_configuration_guard`)
	f.exec(t, `INSERT INTO indexer_lifecycle(indexer_id,enabled,deleted_at) VALUES($1,false,clock_timestamp())`, f.id)
	f.exec(t, `UPDATE indexer SET api_key_encrypted='broken' WHERE id=$1`, f.id)
	f.exec(t, `UPDATE indexer_configuration SET url_encrypted='broken' WHERE indexer_id=$1`, f.id)
	f.s = postgres.NewSiteCheckinConfigStore(f.p, idgen.New(), nil)
	in := f.input()
	in.Enabled = false
	in.ExpectedVersion = 1
	disabled := f.save(t, in, "off")
	if disabled.Config.Version != 2 || disabled.Config.Enabled {
		t.Fatal(disabled)
	}
	replay, e := f.s.ReadSiteCheckinConfigReceipt(t.Context(), f.a, f.id, "first")
	if e != nil || !reflect.DeepEqual(replay, original) {
		t.Fatal(replay, e)
	}
	command, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "first")
	if e != nil || !reflect.DeepEqual(command, original) {
		t.Fatal(command, e)
	}
}
func TestSiteCheckinConfigRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event"} {
		t.Run(table, func(t *testing.T) {
			f := newCheckinFixture(t)
			f.exec(t, fmt.Sprintf(`CREATE FUNCTION fail_checkin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture'; END $$; CREATE TRIGGER fail_checkin BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION fail_checkin()`, table))
			_, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "rollback")
			checkinErr(t, e, ports.ErrUnavailable)
			f.counts(t, 0)
		})
	}
	for _, target := range []string{"role", "source", "configuration", "config", "receipt", "input", "deferred"} {
		t.Run(target, func(t *testing.T) {
			f := newCheckinFixture(t)
			if target == "receipt" || target == "input" {
				f.exec(t, `ALTER TABLE site_checkin_config_receipt DISABLE TRIGGER site_checkin_receipt_history_guard`)
			}
			body := `UPDATE app_user SET role='member' WHERE id='` + f.a.ID + `';`
			switch target {
			case "source":
				body = `UPDATE indexer SET config_version=config_version+1 WHERE id='` + f.id.String() + `';`
			case "configuration":
				body = `UPDATE indexer_configuration SET url_encrypted='tamper' WHERE indexer_id='` + f.id.String() + `';`
			case "config":
				body = `UPDATE site_checkin_config SET daily_at='09:01';`
			case "receipt":
				body = `UPDATE site_checkin_config_receipt SET snapshot=jsonb_set(snapshot,'{daily_at}','"09:01"');`
			case "input":
				body = `UPDATE site_checkin_config_receipt SET input=input-'expected_version';`
			case "deferred":
				body = `RAISE EXCEPTION 'deferred fixture';`
			}
			f.exec(t, `CREATE FUNCTION tamper_checkin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` RETURN NEW; END $$`)
			trigger := `CREATE TRIGGER tamper_checkin AFTER INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION tamper_checkin()`
			if target == "deferred" {
				trigger = `CREATE CONSTRAINT TRIGGER tamper_checkin AFTER INSERT ON outbox_event DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION tamper_checkin()`
			}
			f.exec(t, trigger)
			_, e := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "tamper")
			if e == nil {
				t.Fatal("accepted tamper")
			}
			f.counts(t, 0)
		})
	}
}
func TestSiteCheckinConfigIntegrityPG(t *testing.T) {
	for _, kind := range []string{"timezone", "corrupt", "missing", "isolation", "protected"} {
		t.Run(kind, func(t *testing.T) {
			f := newCheckinFixture(t)
			r := f.save(t, f.input(), "first")
			switch kind {
			case "timezone":
				f.exec(t, `ALTER TABLE site_checkin_config_receipt DISABLE TRIGGER site_checkin_receipt_history_guard`)
				f.exec(t, `UPDATE site_checkin_config_receipt SET snapshot=jsonb_set(jsonb_set(snapshot,'{created_at}',to_jsonb(to_char((snapshot->>'created_at')::timestamptz AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD"T"HH24:MI:SS.US')||'+08:00')),'{updated_at}',to_jsonb(to_char((snapshot->>'updated_at')::timestamptz AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD"T"HH24:MI:SS.US')||'+08:00'))`)
				f.exec(t, `ALTER TABLE site_checkin_config_receipt ENABLE TRIGGER site_checkin_receipt_history_guard`)
				c, e := f.s.ReadSiteCheckinConfig(t.Context(), f.a, f.id)
				if e != nil || !c.CreatedAt.Equal(r.Config.CreatedAt) || c.CreatedAt.Format("-07:00") != "+08:00" {
					t.Fatal(c, e)
				}
			case "corrupt":
				f.exec(t, `UPDATE site_checkin_config SET daily_at='09:01'`)
				_, e := f.s.ReadSiteCheckinConfig(t.Context(), f.a, f.id)
				checkinErr(t, e, ports.ErrEndpointVersionConflict)
			case "missing":
				f.exec(t, `ALTER TABLE site_checkin_config_receipt DISABLE TRIGGER site_checkin_receipt_history_guard`)
				f.exec(t, `DELETE FROM site_checkin_config_receipt`)
				_, e := f.s.ReadSiteCheckinConfig(t.Context(), f.a, f.id)
				checkinErr(t, e, ports.ErrUnavailable)
			case "isolation":
				f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id='11111111-1111-1111-1111-111111111111' WHERE singleton`)
				_, e := f.s.ReadSiteCheckinConfig(t.Context(), f.a, f.id)
				checkinErr(t, e, ports.ErrUnavailable)
				_, e = f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "first")
				checkinErr(t, e, ports.ErrUnavailable)
			case "protected":
				for _, sql := range []string{`DELETE FROM site_checkin_config`, `TRUNCATE site_checkin_config CASCADE`, `UPDATE site_checkin_config_receipt SET command_key='changed'`, `DELETE FROM site_checkin_config_receipt`, `TRUNCATE site_checkin_config_receipt`} {
					if _, e := f.p.Exec(t.Context(), sql); e == nil {
						t.Fatal("unprotected", sql)
					}
				}
			}
		})
	}
	f := newCheckinFixture(t)
	f.p.Close()
	_, e := f.s.ReadSiteCheckinConfig(t.Context(), f.a, f.id)
	checkinErr(t, e, ports.ErrUnavailable)
}
func TestSiteCheckinConfigMigrationPG(t *testing.T) {
	// The historical 99 contract must execute independently of runtime tables.
	history99 := t.TempDir()
	entries, err := os.ReadDir("../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || len(entry.Name()) < 5 || entry.Name()[:5] > "00099" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../../../migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(history99, entry.Name()), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := newCheckinFixtureAt(t, history99, 99)
	v, e := postgres.Rollback(t.Context(), f.dsn, history99)
	if e != nil || v != 98 {
		t.Fatal(v, e)
	}
	v, e = postgres.Migrate(t.Context(), f.dsn, history99)
	if e != nil || v != 99 {
		t.Fatal(v, e)
	}
	// Seed only the historical 99 row; current production writers also require schema100.
	f.exec(t, `INSERT INTO site_checkin_config(indexer_id,expected_account_id,profile,enabled,timezone,daily_at,version,source_config_version) VALUES($1,'42','nexusphp_1103_attendance_v1',true,'UTC','09:00',1,1)`, f.id)
	_, e = postgres.Rollback(t.Context(), f.dsn, history99)
	if e == nil {
		t.Fatal("history downgrade allowed")
	}
	var preserved int
	if e = f.p.QueryRow(t.Context(), `SELECT count(*) FROM site_checkin_config WHERE indexer_id=$1`, f.id).Scan(&preserved); e != nil || preserved != 1 {
		t.Fatal("historical configuration lost", preserved, e)
	}
}

type checkinSecrets struct{ key []byte }

func (s *checkinSecrets) EncryptSecret(b []byte) ([]byte, error) { return crypto.Encrypt(s.key, b) }
func (s *checkinSecrets) DecryptSecret(b []byte) ([]byte, error) { return crypto.Decrypt(s.key, b) }
