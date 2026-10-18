package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *configurationSecrets) DigestSecret([]byte) (string, error) { return "unused", nil }

type configurationSecrets struct {
	key      []byte
	encrypts int
	fail     bool
}

func (s *configurationSecrets) EncryptSecret(p []byte) ([]byte, error) {
	s.encrypts++
	if s.fail {
		return nil, errors.New("secret URL or credential must be suppressed")
	}
	return crypto.Encrypt(s.key, p)
}
func (s *configurationSecrets) DecryptSecret(p []byte) ([]byte, error) {
	return crypto.Decrypt(s.key, p)
}

func configurationFixture(t *testing.T) (*pgxpool.Pool, string, *configurationSecrets) {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	m5IndexerSchema84(t, dsn)
	pool, err := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, dsn, &configurationSecrets{key: bytes.Repeat([]byte{37}, 32)}
}
func configurationExec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func configurationRow(t *testing.T, p *pgxpool.Pool, s *configurationSecrets, raw string) db.Indexer {
	t.Helper()
	cipher, err := s.EncryptSecret([]byte("Cookie: private-credential"))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	configurationExec(t, p, `INSERT INTO indexer(id,name,url,api_key_encrypted,categories,config_version,health,rate_limit_until,next_query_at,rss_enabled,rss_kind,rss_url_encrypted,rss_interval_seconds,rss_next_poll_at,rss_last_success_at,health_checked_at,health_observed_at,health_next_check_at,health_failures,health_reason) VALUES($1,'legacy',$2,$3,'[2000]',7,'rate_limited','2030-01-01','2030-01-02',true,'torznab',$3,900,'2030-01-03','2020-01-01','2020-01-02','2020-01-03','2030-01-04',3,'rate_limited')`, id, raw, cipher)
	row, err := db.New(p).GetIndexerByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
func configurationClassify(raw, _ string) ports.IndexerProtocol {
	if strings.Contains(raw, "nexus") {
		return ports.IndexerProtocolNexusPHP
	}
	if strings.Contains(raw, "feed") {
		return ports.IndexerProtocolRSS
	}
	return ports.IndexerProtocolTorznab
}

func TestIndexerConfigurationConversionPG(t *testing.T) {
	p, dsn, s := configurationFixture(t)
	ctx := t.Context()
	q := db.New(p)
	var original []db.Indexer
	for _, raw := range []string{"https://site.test/api?apikey=private-url", "https://site.test/nexus?passkey=private-url", "https://site.test/feed?token=private-url"} {
		original = append(original, configurationRow(t, p, s, raw))
	}
	calls := 0
	classify := func(raw, key string) ports.IndexerProtocol {
		calls++
		if key != "Cookie: private-credential" {
			t.Fatal("credential mismatch")
		}
		return configurationClassify(raw, key)
	}
	before := s.encrypts
	if err := postgres.EncryptLegacyIndexerConfigurations(ctx, p, s, classify); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || s.encrypts != before+3 {
		t.Fatal("classification/encryption count")
	}
	for _, old := range original {
		stored, err := q.GetIndexerByID(ctx, old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Url != "https://site.test" {
			t.Fatal("origin mismatch")
		}
		reconstructed, modes, err := postgres.ResolveIndexerConfiguration(ctx, q, s, stored)
		if err != nil || !reflect.DeepEqual(old, reconstructed) {
			t.Fatal("logical row or unrelated fields changed", err)
		}
		if !modes.LegacyV1 || modes.Protocol != configurationClassify(old.Url, "") {
			t.Fatal("persisted protocol mismatch")
		}
		config, err := q.GetIndexerConfiguration(ctx, old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(config.UrlEncrypted, []byte("private-url")) {
			t.Fatal("plaintext URL in ciphertext")
		}
		if _, _, err = postgres.ResolveIndexerConfiguration(ctx, q, nil, stored); err != ports.ErrUnavailable {
			t.Fatal("nil decoder accepted")
		}
		wrong := &configurationSecrets{key: bytes.Repeat([]byte{9}, 32)}
		if _, _, err = postgres.ResolveIndexerConfiguration(ctx, q, wrong, stored); err != ports.ErrUnavailable {
			t.Fatal("wrong key accepted")
		}
		raw, _, err := postgres.ResolveIndexerURL(ctx, q, s, stored.ID, stored.ConfigVersion+1, stored.Url)
		if err != nil || raw != old.Url {
			t.Fatal("independent RSS version rejected")
		}
		for _, version := range []int64{0, stored.ConfigVersion - 1} {
			if _, _, err = postgres.ResolveIndexerURL(ctx, q, s, stored.ID, version, stored.Url); err != ports.ErrUnavailable {
				t.Fatal("future extension accepted")
			}
		}
		if _, _, err = postgres.ResolveIndexerURL(ctx, q, s, stored.ID, stored.ConfigVersion, "https://other.test"); err != ports.ErrUnavailable {
			t.Fatal("origin mismatch accepted")
		}
	}
	before = s.encrypts
	if err := postgres.EncryptLegacyIndexerConfigurations(ctx, p, s, classify); err != nil {
		t.Fatal(err)
	}
	if s.encrypts != before || calls != 3 {
		t.Fatal("repeat re-encrypted or reclassified")
	}
	var audits int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='indexer.configuration.encrypted' AND actor_type='system' AND payload='{}'::jsonb`).Scan(&audits); err != nil || audits != 3 {
		t.Fatal("safe audits missing", err)
	}
	if _, err := postgres.Rollback(ctx, dsn, "../../../migrations"); err == nil {
		t.Fatal("encrypted downgrade accepted")
	}
	configurationExec(t, p, "DELETE FROM indexer")
	if _, err := postgres.Rollback(ctx, dsn, "../../../migrations"); err == nil {
		t.Fatal("history downgrade accepted")
	}
}

func TestIndexerConfigurationRollbackPG(t *testing.T) {
	for _, fault := range []string{"encrypt", "audit"} {
		t.Run(fault, func(t *testing.T) {
			p, _, s := configurationFixture(t)
			old := configurationRow(t, p, s, "https://site.test/api?secret=private-url")
			if fault == "encrypt" {
				s.fail = true
			} else {
				configurationExec(t, p, `CREATE FUNCTION reject_configuration_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private-url'; END $$; CREATE TRIGGER reject_configuration_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_configuration_audit()`)
			}
			err := postgres.EncryptLegacyIndexerConfigurations(t.Context(), p, s, configurationClassify)
			if err != ports.ErrUnavailable {
				t.Fatal("unsafe error", err)
			}
			row, err := db.New(p).GetIndexerByID(t.Context(), old.ID)
			if err != nil || !reflect.DeepEqual(row, old) {
				t.Fatal("failed transaction changed base row", err)
			}
			var count int
			if err = p.QueryRow(t.Context(), "SELECT count(*) FROM indexer_configuration").Scan(&count); err != nil || count != 0 {
				t.Fatal("partial extension committed", err)
			}
		})
	}
}

func TestIndexerConfigurationHistoricalAndRoundtripPG(t *testing.T) {
	p, dsn, s := configurationFixture(t)
	ctx := t.Context()
	available, err := postgres.IndexerConfigurationSchemaAvailable(ctx, p)
	if err != nil || !available {
		t.Fatal("schema unavailable", err)
	}
	if _, err = postgres.Rollback(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	old := configurationRow(t, p, s, "https://site.test/api?secret=private-url")
	q := db.New(p)
	available, err = postgres.IndexerConfigurationSchemaAvailable(ctx, p)
	if err != nil || available {
		t.Fatal("historical probe", err)
	}
	row, modes, err := postgres.ResolveIndexerConfiguration(ctx, q, nil, old)
	if err != nil || !reflect.DeepEqual(row, old) || !modes.LegacyV1 {
		t.Fatal("historical fixture rejected", err)
	}
	if _, _, err = postgres.ResolveIndexerConfiguration(ctx, q, s, old); err != ports.ErrUnavailable {
		t.Fatal("historical schema accepted with codec")
	}
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, p, s, configurationClassify); err != ports.ErrUnavailable {
		t.Fatal("migration accepted missing schema")
	}
	m5IndexerSchema84(t, dsn)
	if _, _, err = postgres.ResolveIndexerConfiguration(ctx, q, s, old); err != ports.ErrUnavailable {
		t.Fatal("missing extension accepted")
	}
	fixture, fixtureModes, fixtureErr := postgres.ResolveIndexerConfiguration(ctx, q, nil, old)
	if fixtureErr != nil || !reflect.DeepEqual(fixture, old) || !fixtureModes.LegacyV1 || fixtureModes.Protocol != "" {
		t.Fatal("missing extension fixture rejected", fixtureErr)
	}
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, p, nil, configurationClassify); err != ports.ErrUnavailable {
		t.Fatal("nil codec accepted")
	}
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, p, s, nil); err != ports.ErrUnavailable {
		t.Fatal("nil classifier accepted")
	}
	wrong := &configurationSecrets{key: bytes.Repeat([]byte{2}, 32)}
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, p, wrong, configurationClassify); err != ports.ErrUnavailable {
		t.Fatal("wrong credential key accepted")
	}
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, p, s, func(string, string) ports.IndexerProtocol { return "invalid" }); err != ports.ErrUnavailable {
		t.Fatal("invalid protocol accepted")
	}
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, p, s, configurationClassify); err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// The resolver must see an uncommitted extension edit through the supplied q.
	if _, err = tx.Exec(ctx, "UPDATE indexer_configuration SET behavior_version='explicit-v1' WHERE indexer_id=$1", old.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := db.New(tx).GetIndexerByID(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, modes, err = postgres.ResolveIndexerConfiguration(ctx, db.New(tx), s, stored)
	if err != nil || modes.LegacyV1 {
		t.Fatal("resolver escaped transaction", err)
	}
}
