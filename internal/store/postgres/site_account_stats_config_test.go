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

type statsFixture struct {
	p       *pgxpool.Pool
	s       *postgres.SiteAccountStatsConfigStore
	a       *ports.Actor
	id      uuid.UUID
	dsn     string
	secrets *statsSecrets
}

func newStatsFixture(t *testing.T) *statsFixture {
	return newStatsFixtureAt(t, "../../../migrations", postgres.CurrentMigrationVersion)
}

func newStatsFixtureAt(t *testing.T, migrationDir string, expectedVersion int64) *statsFixture {
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
	f := &statsFixture{p: p, id: uuid.New(), dsn: dsn, secrets: &statsSecrets{key: bytes.Repeat([]byte{41}, 32)}}
	f.a = f.user(t)
	cookie, e := f.secrets.EncryptSecret([]byte("session=fixture"))
	if e != nil {
		t.Fatal(e)
	}
	root, e := f.secrets.EncryptSecret([]byte("https://nexus.test/root/"))
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'stats','https://nexus.test',$2)`, f.id, cookie)
	f.exec(t, `INSERT INTO indexer_configuration(indexer_id,protocol,url_encrypted,behavior_version,configuration_version) VALUES($1,'nexusphp',$2,'explicit-v1',1)`, f.id, root)
	f.s = postgres.NewSiteAccountStatsConfigStore(p, idgen.New(), f.secrets)
	return f
}
func (f *statsFixture) user(t *testing.T) *ports.Actor {
	t.Helper()
	u, e := db.New(f.p).CreateUser(t.Context(), db.CreateUserParams{ID: uuid.New(), Username: uuid.NewString(), PasswordHash: "fixture", Role: "admin", IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	return &ports.Actor{Type: ports.ActorTypeUser, ID: u.ID.String(), Role: "admin"}
}
func (f *statsFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, e := f.p.Exec(t.Context(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func (f *statsFixture) input() ports.SiteAccountStatsConfigInput {
	return ports.SiteAccountStatsConfigInput{ExpectedAccountID: "18446744073709551615", Profile: ports.NexusPHPClassicAccountStatsProfile, Enabled: true, IntervalSeconds: 900, ExpectedSourceVersion: 1}
}
func (f *statsFixture) save(t *testing.T, in ports.SiteAccountStatsConfigInput, key string) *ports.SiteAccountStatsConfigReceipt {
	t.Helper()
	r, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, in, key)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func statsErr(t *testing.T, e, want error) {
	t.Helper()
	if !errors.Is(e, want) {
		t.Fatalf("error=%v want=%v", e, want)
	}
}
func (f *statsFixture) counts(t *testing.T, want int) {
	t.Helper()
	for _, table := range []string{"site_account_stats_config", "site_account_stats_config_receipt", "audit_log", "outbox_event"} {
		var n int
		if e := f.p.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != want {
			t.Fatalf("%s: %d want %d err %v", table, n, want, e)
		}
	}
}

func TestSiteAccountStatsConfigAuthorizationPG(t *testing.T) {
	f := newStatsFixture(t)
	ctx := t.Context()
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeUser, ID: f.a.ID, Role: "member"}, {Type: ports.ActorTypeUser, ID: f.a.ID, Role: "admin", Scopes: []string{}}, {Type: ports.ActorTypeSystem, ID: f.a.ID, Role: "admin"}, {Type: ports.ActorTypeUser, ID: "bad", Role: "admin"}} {
		want := ports.ErrForbidden
		if a == nil {
			want = ports.ErrUnauthorized
		}
		_, e := f.s.ReadSiteAccountStatsConfig(ctx, a, f.id)
		statsErr(t, e, want)
		_, e = f.s.ReadSiteAccountStatsConfigReceipt(ctx, a, f.id, "x")
		statsErr(t, e, want)
		_, e = f.s.SaveSiteAccountStatsConfig(ctx, a, f.id, f.input(), "x")
		statsErr(t, e, want)
	}
	if r, e := f.s.ReadSiteAccountStatsConfig(ctx, f.a, f.id); r != nil || e != nil {
		t.Fatal(r, e)
	}
	_, e := f.s.ReadSiteAccountStatsConfig(ctx, f.a, uuid.New())
	statsErr(t, e, ports.ErrNotFound)
	original := f.save(t, f.input(), "first")
	other := f.user(t)
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
	c, e := f.s.ReadSiteAccountStatsConfig(ctx, other, f.id)
	if e != nil || !reflect.DeepEqual(c, &original.Config) {
		t.Fatal(c, e)
	}
	_, e = f.s.ReadSiteAccountStatsConfigReceipt(ctx, other, f.id, "first")
	statsErr(t, e, ports.ErrNotFound)
}
func TestSiteAccountStatsConfigRevocationAfterWaitPG(t *testing.T) {
	f := newStatsFixture(t)
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(t.Context(), `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID)); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, f.input(), "wait"); done <- e }()
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
	statsErr(t, <-done, ports.ErrForbidden)
	f.counts(t, 0)
}
func TestSiteAccountStatsConfigConcurrencyPG(t *testing.T) {
	f := newStatsFixture(t)
	var wg sync.WaitGroup
	results := make(chan *ports.SiteAccountStatsConfigReceipt, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			r, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, f.input(), "same")
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
	changed.IntervalSeconds = 901
	_, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, changed, "same")
	statsErr(t, e, ports.ErrEndpointVersionConflict)
	_, e = f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, changed, "new")
	statsErr(t, e, ports.ErrEndpointVersionConflict)
	f.exec(t, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, f.id)
	f.exec(t, `ALTER TABLE indexer_configuration DISABLE TRIGGER indexer_management_configuration_guard`)
	f.exec(t, `ALTER TABLE indexer_configuration DISABLE TRIGGER indexer_management_configuration_guard`)
	f.exec(t, `UPDATE indexer_configuration SET url_encrypted='broken' WHERE indexer_id=$1`, f.id)
	replay, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, f.input(), "same")
	if e != nil || !reflect.DeepEqual(a, replay) {
		t.Fatal(replay, e)
	}
}
func TestSiteAccountStatsConfigValidationPG(t *testing.T) {
	f := newStatsFixture(t)
	f.exec(t, `ALTER TABLE indexer_configuration DISABLE TRIGGER indexer_management_configuration_guard`)
	for _, uid := range []string{"0", "01", "+1", "18446744073709551616", " 1"} {
		in := f.input()
		in.ExpectedAccountID = uid
		_, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, in, "key")
		statsErr(t, e, ports.ErrRejected)
	}
	for _, key := range []string{"", "bad/key", "空", string(bytes.Repeat([]byte{'x'}, 161))} {
		_, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, f.input(), key)
		statsErr(t, e, ports.ErrRejected)
	}
	for _, raw := range []string{"https://nexus.test/?x=1", "https://u:p@nexus.test/", "https://nexus.test/a/../b", "https://nexus.test/%2e", "https://nexus.test/#x", "https://nexus.test:0/"} {
		cipher, e := f.secrets.EncryptSecret([]byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		f.exec(t, `UPDATE indexer_configuration SET url_encrypted=$2 WHERE indexer_id=$1`, f.id, cipher)
		_, e = f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, f.input(), "url")
		want := ports.ErrRejected
		if raw == "https://nexus.test:0/" || raw == "https://nexus.test/#x" {
			want = ports.ErrUnavailable
		}
		statsErr(t, e, want)
	}
	f.exec(t, `UPDATE indexer_configuration SET protocol='torznab' WHERE indexer_id=$1`, f.id)
	in := f.input()
	in.Enabled = false
	_, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, in, "protocol")
	statsErr(t, e, ports.ErrRejected)
	f.counts(t, 0)
}
func TestSiteAccountStatsConfigDisableAndHistoryPG(t *testing.T) {
	f := newStatsFixture(t)
	original := f.save(t, f.input(), "first")
	f.exec(t, `ALTER TABLE indexer_configuration DISABLE TRIGGER indexer_management_configuration_guard`)
	f.exec(t, `INSERT INTO indexer_lifecycle(indexer_id,enabled,deleted_at) VALUES($1,false,clock_timestamp())`, f.id)
	f.exec(t, `UPDATE indexer SET api_key_encrypted='broken' WHERE id=$1`, f.id)
	f.exec(t, `UPDATE indexer_configuration SET url_encrypted='broken' WHERE indexer_id=$1`, f.id)
	f.s = postgres.NewSiteAccountStatsConfigStore(f.p, idgen.New(), nil)
	in := f.input()
	in.Enabled = false
	in.ExpectedVersion = 1
	disabled := f.save(t, in, "off")
	if disabled.Config.Version != 2 || disabled.Config.Enabled {
		t.Fatal(disabled)
	}
	replay, e := f.s.ReadSiteAccountStatsConfigReceipt(t.Context(), f.a, f.id, "first")
	if e != nil || !reflect.DeepEqual(replay, original) {
		t.Fatal(replay, e)
	}
}
func TestSiteAccountStatsConfigRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event"} {
		t.Run(table, func(t *testing.T) {
			f := newStatsFixture(t)
			f.exec(t, fmt.Sprintf(`CREATE FUNCTION fail_stats() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture'; END $$; CREATE TRIGGER fail_stats BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION fail_stats()`, table))
			_, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, f.input(), "rollback")
			statsErr(t, e, ports.ErrUnavailable)
			f.counts(t, 0)
		})
	}
	for _, target := range []string{"role", "source", "configuration", "deferred"} {
		t.Run(target, func(t *testing.T) {
			f := newStatsFixture(t)
			body := `UPDATE app_user SET role='member' WHERE id='` + f.a.ID + `';`
			switch target {
			case "source":
				body = `UPDATE indexer SET config_version=config_version+1 WHERE id='` + f.id.String() + `';`
			case "configuration":
				body = `UPDATE indexer_configuration SET url_encrypted='tamper' WHERE indexer_id='` + f.id.String() + `';`
			case "deferred":
				body = `RAISE EXCEPTION 'deferred fixture';`
			}
			f.exec(t, `CREATE FUNCTION tamper_stats() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` RETURN NEW; END $$`)
			trigger := `CREATE TRIGGER tamper_stats AFTER INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION tamper_stats()`
			if target == "deferred" {
				trigger = `CREATE CONSTRAINT TRIGGER tamper_stats AFTER INSERT ON outbox_event DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION tamper_stats()`
			}
			f.exec(t, trigger)
			_, e := f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, f.input(), "tamper")
			if e == nil {
				t.Fatal("accepted tamper")
			}
			f.counts(t, 0)
		})
	}
}
func TestSiteAccountStatsConfigIntegrityPG(t *testing.T) {
	for _, kind := range []string{"timezone", "corrupt", "missing", "isolation", "protected"} {
		t.Run(kind, func(t *testing.T) {
			f := newStatsFixture(t)
			r := f.save(t, f.input(), "first")
			switch kind {
			case "timezone":
				f.exec(t, `ALTER TABLE site_account_stats_config_receipt DISABLE TRIGGER site_account_stats_receipt_history_guard`)
				f.exec(t, `UPDATE site_account_stats_config_receipt SET snapshot=jsonb_set(jsonb_set(snapshot,'{created_at}',to_jsonb(to_char((snapshot->>'created_at')::timestamptz AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD"T"HH24:MI:SS.US')||'+08:00')),'{updated_at}',to_jsonb(to_char((snapshot->>'updated_at')::timestamptz AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD"T"HH24:MI:SS.US')||'+08:00'))`)
				f.exec(t, `ALTER TABLE site_account_stats_config_receipt ENABLE TRIGGER site_account_stats_receipt_history_guard`)
				c, e := f.s.ReadSiteAccountStatsConfig(t.Context(), f.a, f.id)
				if e != nil || !c.CreatedAt.Equal(r.Config.CreatedAt) || c.CreatedAt.Format("-07:00") != "+08:00" {
					t.Fatal(c, e)
				}
			case "corrupt":
				f.exec(t, `UPDATE site_account_stats_config SET interval_seconds=901`)
				_, e := f.s.ReadSiteAccountStatsConfig(t.Context(), f.a, f.id)
				statsErr(t, e, ports.ErrEndpointVersionConflict)
			case "missing":
				f.exec(t, `ALTER TABLE site_account_stats_config_receipt DISABLE TRIGGER site_account_stats_receipt_history_guard`)
				f.exec(t, `DELETE FROM site_account_stats_config_receipt`)
				_, e := f.s.ReadSiteAccountStatsConfig(t.Context(), f.a, f.id)
				statsErr(t, e, ports.ErrUnavailable)
			case "isolation":
				f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id='11111111-1111-1111-1111-111111111111' WHERE singleton`)
				_, e := f.s.ReadSiteAccountStatsConfig(t.Context(), f.a, f.id)
				statsErr(t, e, ports.ErrUnavailable)
				_, e = f.s.SaveSiteAccountStatsConfig(t.Context(), f.a, f.id, f.input(), "first")
				statsErr(t, e, ports.ErrUnavailable)
			case "protected":
				for _, sql := range []string{`DELETE FROM site_account_stats_config`, `TRUNCATE site_account_stats_config CASCADE`, `UPDATE site_account_stats_config_receipt SET command_key='changed'`, `DELETE FROM site_account_stats_config_receipt`, `TRUNCATE site_account_stats_config_receipt`} {
					if _, e := f.p.Exec(t.Context(), sql); e == nil {
						t.Fatal("unprotected", sql)
					}
				}
			}
		})
	}
	f := newStatsFixture(t)
	f.p.Close()
	_, e := f.s.ReadSiteAccountStatsConfig(t.Context(), f.a, f.id)
	statsErr(t, e, ports.ErrUnavailable)
}
func TestSiteAccountStatsConfigMigrationPG(t *testing.T) {
	// The historical 97 contract must execute independently of runtime tables.
	history97 := t.TempDir()
	entries, err := os.ReadDir("../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || len(entry.Name()) < 5 || entry.Name()[:5] > "00097" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../../../migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(history97, entry.Name()), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := newStatsFixtureAt(t, history97, 97)
	v, e := postgres.Rollback(t.Context(), f.dsn, history97)
	if e != nil || v != 96 {
		t.Fatal(v, e)
	}
	v, e = postgres.Migrate(t.Context(), f.dsn, history97)
	if e != nil || v != 97 {
		t.Fatal(v, e)
	}
	f.save(t, f.input(), "history")
	_, e = postgres.Rollback(t.Context(), f.dsn, history97)
	if e == nil {
		t.Fatal("history downgrade allowed")
	}
	f.counts(t, 1)
}

type statsSecrets struct{ key []byte }

func (s *statsSecrets) EncryptSecret(b []byte) ([]byte, error) { return crypto.Encrypt(s.key, b) }
func (s *statsSecrets) DecryptSecret(b []byte) ([]byte, error) { return crypto.Decrypt(s.key, b) }
