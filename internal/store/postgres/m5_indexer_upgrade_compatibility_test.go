package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

// Keep the original schema64/65 fixtures intact. Only this test's database is
// advanced to the real pre-M5 schema before any authority is frozen.
func m5UpgradeSchema83(t *testing.T, f *frozenUpgradeFixture) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join("..", "..", "..", "migrations")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal("read migration directory failed")
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 6 || name[:5] > "00083" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal("read migration failed")
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal("copy migration failed")
		}
	}
	if v, err := postgres.Migrate(t.Context(), f.dsn, dir); err != nil || v != 83 {
		t.Fatalf("migrate83: version=%d error=%v", v, err)
	}
}

func m5UpgradeSource(t *testing.T, f *frozenUpgradeFixture, codec *crypto.SecretProtector) string {
	t.Helper()
	// Reserved .invalid host; these tests never construct a network client.
	raw := "https://upgrade-user:synthetic-password@indexer.invalid/private/torznab/api?apikey=synthetic-url-key&cat=2000&passkey=synthetic-passkey"
	cipher, err := codec.EncryptSecret([]byte("synthetic-indexer-api-key"))
	if err != nil {
		t.Fatal("encrypt fixture credential failed")
	}
	f.exec(t, "UPDATE indexer SET url=$2,api_key_encrypted=$3 WHERE id=$1", f.source, raw, cipher)
	return raw
}

func m5UpgradePreview(t *testing.T, f *frozenUpgradeFixture, decoder ports.CredentialDecoder) *ports.UpgradePreviewSnapshot {
	t.Helper()
	out, err := postgres.NewUpgradePreviewStore(f.p, decoder).ReadUpgradePreview(t.Context(), f.actor, ports.UpgradePreviewRequest{SubscriptionID: f.sub, CurrentFileID: f.file, CandidateID: f.candidate})
	if err != nil || out == nil || out.Version.InputStamp == "" {
		t.Fatalf("real upgrade preview failed: %v", err)
	}
	return out
}

// Compare deterministic PostgreSQL JSON text byte for byte, including all row
// fields, timestamps, ciphertexts, receipt bodies, snapshots and job payloads.
// The conversion's one new system audit is compared separately below.
func m5UpgradeFrozenRows(t *testing.T, f *frozenUpgradeFixture) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, table := range []string{
		"movie_upgrade_authority", "acquisition_reservation", "unit_reservation",
		"download_requirement", "job", "download_task", "movie_upgrade_preparation",
		"movie_upgrade_preparation_lease", "idempotency_record", "outbox_event",
		"library_root", "library_file", "library_file_unit", "unit_fact", "media_unit",
		"subscription", "wanted_item",
	} {
		var rows string
		if err := f.p.QueryRow(t.Context(), "SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]'::jsonb)::text FROM "+table+" x").Scan(&rows); err != nil {
			t.Fatalf("read frozen %s: %v", table, err)
		}
		out[table] = rows
	}
	var audits string
	if err := f.p.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]'::jsonb)::text FROM audit_log x WHERE action<>'indexer.configuration.encrypted'`).Scan(&audits); err != nil {
		t.Fatal("read historical audits failed")
	}
	out["audit_log"] = audits
	return out
}

func m5UpgradeAssertFrozen(t *testing.T, f *frozenUpgradeFixture, want map[string]string) {
	t.Helper()
	got := m5UpgradeFrozenRows(t, f)
	for table, rows := range want {
		if got[table] != rows {
			t.Errorf("frozen %s rows changed", table)
		}
	}
}

func m5UpgradeConvert(t *testing.T, f *frozenUpgradeFixture, codec *crypto.SecretProtector, raw string) {
	t.Helper()
	q := db.New(f.p)
	old, err := q.GetIndexerByID(t.Context(), f.source)
	if err != nil || old.Url != raw {
		t.Fatal("read original source failed")
	}
	m5IndexerSchema84(t, f.dsn)
	if err := postgres.EncryptLegacyIndexerConfigurations(t.Context(), f.p, codec, search.ClassifyLegacyIndexerProtocol); err != nil {
		t.Fatalf("convert source: %v", err)
	}
	stored, err := q.GetIndexerByID(t.Context(), f.source)
	if err != nil || stored.Url != "https://indexer.invalid" {
		t.Fatal("source origin was not replaced safely")
	}
	resolved, modes, err := postgres.ResolveIndexerConfiguration(t.Context(), q, codec, stored)
	if err != nil || !reflect.DeepEqual(old, resolved) || !modes.LegacyV1 || modes.Protocol != ports.IndexerProtocolTorznab {
		t.Fatal("conversion changed logical source, credential ciphertext, version, or timestamps")
	}
	config, err := q.GetIndexerConfiguration(t.Context(), f.source)
	if err != nil || bytes.Contains(config.UrlEncrypted, []byte(raw)) || len(config.UrlEncrypted) == 0 {
		t.Fatal("encrypted URL extension missing or unsafe")
	}
	plaintext, err := codec.DecryptSecret(config.UrlEncrypted)
	if err != nil || string(plaintext) != raw {
		t.Fatal("encrypted URL did not preserve exact original")
	}
	frozen := m5UpgradeFrozenRows(t, f)
	if err := postgres.EncryptLegacyIndexerConfigurations(t.Context(), f.p, codec, search.ClassifyLegacyIndexerProtocol); err != nil {
		t.Fatalf("repeat conversion: %v", err)
	}
	repeated, err := q.GetIndexerConfiguration(t.Context(), f.source)
	if err != nil || !reflect.DeepEqual(config, repeated) {
		t.Fatal("repeat conversion rewrote URL envelope")
	}
	m5UpgradeAssertFrozen(t, f, frozen)
	var audits int
	if err := f.p.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE action='indexer.configuration.encrypted' AND actor_type='system' AND payload='{}'::jsonb`).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("conversion must have exactly one safe system audit")
	}
}

func TestM5IndexerEncryptionPreservesFrozenUpgradeReservation(t *testing.T) {
	f := frozenUpgrade(t)
	m5UpgradeSchema83(t, f)
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{41}, 32))
	raw := m5UpgradeSource(t, f, codec)
	f.enable(t)
	command := f.command(t)
	reserved, err := f.store.ReserveUpgrade(t.Context(), f.actor, command, "m5-original")
	if err != nil || reserved == nil || reserved.ExecutionAllowed || reserved.Status != "reserved_not_executable" {
		t.Fatalf("real freeze failed: %v", err)
	}
	// Reservation changes facts_version itself; compare the live preview on the
	// same frozen graph on both sides of the URL-only conversion.
	beforePreview := m5UpgradePreview(t, f, nil)
	before := m5UpgradeFrozenRows(t, f)
	var protected, observed string
	if err := f.p.QueryRow(t.Context(), `SELECT snapshot->>'protected_digest',snapshot->'observation'->>'input_stamp' FROM movie_upgrade_authority WHERE operation_id=$1`, reserved.OperationID).Scan(&protected, &observed); err != nil || len(protected) != 64 || observed != command.Expected.InputStamp {
		t.Fatal("actual authority did not freeze preview and protected digest")
	}
	var authorities, lanes, reservations, demands, jobs, tasks, audits, receipts int
	if err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM movie_upgrade_authority),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM job),(SELECT count(*) FROM download_task),(SELECT count(*) FROM audit_log WHERE action='upgrade.reserve'),(SELECT count(*) FROM idempotency_record WHERE idempotency_key='upgrade.reserve:m5-original')`).Scan(&authorities, &lanes, &reservations, &demands, &jobs, &tasks, &audits, &receipts); err != nil || authorities != 1 || lanes != 1 || reservations != 1 || demands != 1 || jobs != 0 || tasks != 0 || audits != 1 || receipts != 1 {
		t.Fatal("real reservation cardinality mismatch")
	}
	m5UpgradeConvert(t, f, codec, raw)
	afterPreview := m5UpgradePreview(t, f, codec)
	if beforePreview.Version != afterPreview.Version || beforePreview.HoldReason != afterPreview.HoldReason {
		t.Fatal("URL encryption changed real preview InputStamp or hold reason")
	}
	m5UpgradeAssertFrozen(t, f, before)
	f.store = postgres.NewUpgradeReservationStore(f.p, idgen.New(), codec)
	for range 3 {
		recovered, err := f.store.ReserveUpgrade(t.Context(), f.actor, command, "m5-original")
		if err != nil || recovered == nil || *recovered != *reserved {
			t.Fatalf("same-key historical reservation recovery failed: %v", err)
		}
		m5UpgradeAssertFrozen(t, f, before)
	}
	for _, decoder := range []ports.CredentialDecoder{nil, crypto.NewSecretProtector(bytes.Repeat([]byte{42}, 32))} {
		out, err := postgres.NewUpgradePreviewStore(f.p, decoder).ReadUpgradePreview(t.Context(), f.actor, ports.UpgradePreviewRequest{SubscriptionID: f.sub, CurrentFileID: f.file, CandidateID: f.candidate})
		if out != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("missing/wrong key accepted a new preview")
		}
	}
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
	if out, err := f.store.ReserveUpgrade(t.Context(), f.actor, command, "m5-original"); out != nil || !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("disabled current account recovered historical reservation")
	}
	m5UpgradeAssertFrozen(t, f, before)
}

// The inspector is the service's I/O boundary. A bad storage key must stop the
// real repository before the service reaches this boundary.
type m5UpgradeNoNetworkInspector struct{ calls int }

func (i *m5UpgradeNoNetworkInspector) InspectMovieUpgradePreparation(context.Context, *ports.MovieUpgradePreparationWork) (ports.MovieUpgradeMetainfoProof, error) {
	i.calls++
	return nil, ports.ErrUnavailable
}

func TestM5IndexerEncryptionPreservesUpgradePreparationSource(t *testing.T) {
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{51}, 32))
	var raw string
	f := preparingUpgradeWithHook(t, func(base *frozenUpgradeFixture) {
		m5UpgradeSchema83(t, base)
		raw = m5UpgradeSource(t, base, codec)
	})
	request := ports.PublicMovieUpgradePreparation{SubscriptionID: f.sub, OperationID: f.operation, Confirmed: true}
	oldWork, err := f.preparation.LoadMovieUpgradePreparationWork(t.Context(), f.actor, request, *f.lease)
	if err != nil || oldWork == nil || oldWork.EndpointURL != raw {
		t.Fatalf("load real pre-conversion work failed: %v", err)
	}
	before := m5UpgradeFrozenRows(t, f.frozenUpgradeFixture)
	beforePreview := m5UpgradePreview(t, f.frozenUpgradeFixture, nil)
	m5UpgradeConvert(t, f.frozenUpgradeFixture, codec, raw)
	m5UpgradeAssertFrozen(t, f.frozenUpgradeFixture, before)
	if after := m5UpgradePreview(t, f.frozenUpgradeFixture, codec); after.Version != beforePreview.Version {
		t.Fatal("preparation preview InputStamp changed after encryption")
	}
	for _, decoder := range []ports.CredentialDecoder{nil, crypto.NewSecretProtector(bytes.Repeat([]byte{52}, 32))} {
		repo, err := postgres.NewUpgradePreparationStore(f.p, idgen.New(), torrent.Inspector{}, decoder)
		if err != nil {
			t.Fatal("construct preparation store failed")
		}
		if work, err := repo.LoadMovieUpgradePreparationWork(t.Context(), f.actor, request, *f.lease); work != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("missing/wrong key returned preparation source")
		}
		inspector := &m5UpgradeNoNetworkInspector{}
		service, err := acquisition.NewPublicUpgradePreparationService(repo, inspector, idgen.New())
		if err != nil {
			t.Fatal("construct preparation service failed")
		}
		if result, err := service.Prepare(t.Context(), f.actor, request, "m5-no-network"); result != nil || !errors.Is(err, ports.ErrUnavailable) || inspector.calls != 0 {
			t.Fatal("missing/wrong key reached preparation I/O boundary")
		}
		m5UpgradeAssertFrozen(t, f.frozenUpgradeFixture, before)
	}
	repo, err := postgres.NewUpgradePreparationStore(f.p, idgen.New(), torrent.Inspector{}, codec)
	if err != nil {
		t.Fatal("construct encrypted preparation store failed")
	}
	work, err := repo.LoadMovieUpgradePreparationWork(t.Context(), f.actor, request, *f.lease)
	if err != nil || work == nil || !reflect.DeepEqual(work, oldWork) {
		t.Fatalf("encrypted storage changed exact original preparation work: %v", err)
	}
	m5UpgradeAssertFrozen(t, f.frozenUpgradeFixture, before)
	// This performs the production protected-digest recomputation and native
	// torrent proof verification; preserved JSON alone cannot make it pass.
	result, err := repo.PrepareMovieUpgrade(t.Context(), f.actor, f.prepare(), "m5-prepare")
	if err != nil || result == nil || result.TaskID == uuid.Nil || result.ExecutionAllowed || result.Status != "prepared_not_sent" {
		t.Fatalf("real preparation after encryption failed: %v", err)
	}
	after := m5UpgradeFrozenRows(t, f.frozenUpgradeFixture)
	for _, table := range []string{"movie_upgrade_authority", "library_root", "library_file", "library_file_unit", "unit_fact", "acquisition_reservation", "unit_reservation", "download_requirement", "job"} {
		if after[table] != before[table] {
			t.Errorf("real preparation changed original protected %s", table)
		}
	}
	for range 2 {
		replayed, err := repo.ReadMovieUpgradePreparation(t.Context(), f.actor, request, "m5-prepare")
		if err != nil || replayed == nil || *replayed != *result {
			t.Fatalf("prepared receipt recovery failed: %v", err)
		}
		m5UpgradeAssertFrozen(t, f.frozenUpgradeFixture, after)
	}
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
	if replayed, err := repo.ReadMovieUpgradePreparation(t.Context(), f.actor, request, "m5-prepare"); replayed != nil || !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("disabled current account recovered historical preparation")
	}
	m5UpgradeAssertFrozen(t, f.frozenUpgradeFixture, after)
}
