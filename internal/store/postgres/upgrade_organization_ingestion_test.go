package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ingestionFixture78(t *testing.T) (*organizationJobFixture, ports.JobLease, *nativeLinkFS, ports.MovieUpgradeOrganizationIngestionRepository) {
	t.Helper()
	f, lease, files, executor := executionFixture77(t)
	if v, e := postgres.Migrate(context.Background(), f.dsn, "../../../migrations"); e != nil || v < 80 {
		t.Fatal(v, e)
	}
	returned, e := executor.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease)
	if e != nil || returned == nil || returned.Status != "linked_pending_ingestion" {
		t.Fatal(returned, e)
	}
	r, e := postgres.NewMovieUpgradeOrganizationIngestionRepository(f.trackingFixture.preparation, files, outbox.New(f.p, idgen.New(), nativeUpgradeClock{}))
	if e != nil {
		t.Fatal(e)
	}
	return f, lease, files, r
}
func assertIngestionAbsent(t *testing.T, f *organizationJobFixture) {
	t.Helper()
	for _, q := range []string{`SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`, `SELECT count(*) FROM outbox_delivery`, `SELECT count(*) FROM notification_batch_member`, `SELECT count(*) FROM movie_upgrade_organization_ingestion`, `SELECT count(*) FROM audit_log WHERE action='upgrade.organization-ingest'`, `SELECT count(*) FROM outbox_event WHERE event_type='upgrade.organization-ingest'`, `SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.organization-ingest:%'`} {
		var n int
		if e := f.p.QueryRow(context.Background(), q).Scan(&n); e != nil || n != 0 {
			t.Fatal(q, n, e)
		}
	}
}
func TestMovieUpgradeOrganizationIngestionPG(t *testing.T) {
	f, lease, files, r := ingestionFixture78(t)
	ctx := context.Background()
	var before string
	protected := `SELECT jsonb_build_object('old',(SELECT to_jsonb(x) FROM library_file x WHERE id=a.old_file_id),'oldfacts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_fact x WHERE source_file_id=a.old_file_id),'oldmapping',(SELECT jsonb_agg(to_jsonb(x) ORDER BY unit_id) FROM library_file_unit x WHERE file_id=a.old_file_id),'lanes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM acquisition_reservation x WHERE operation_id=a.operation_id),'reservations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_reservation x WHERE operation_id=a.operation_id),'requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_requirement x WHERE operation_id=a.operation_id),'task',(SELECT to_jsonb(x) FROM download_task x WHERE operation_id=a.operation_id))::text FROM movie_upgrade_authority a WHERE operation_id=$1`
	if e := f.p.QueryRow(ctx, protected, f.operation).Scan(&before); e != nil {
		t.Fatal(e)
	}
	q := postgres.Queries(f.p)
	gen := idgen.New()
	for range 2 {
		if _, e := q.CreateMediaServer(ctx, db.CreateMediaServerParams{ID: gen.NewUUID(), Name: "upgrade refresh", ServerType: "jellyfin", Url: "http://127.0.0.1:1", ApiKeyEncrypted: []byte("fixture-encrypted"), PathMapping: []byte(`{}`)}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: gen.NewUUID(), Name: "upgrade notification", ChannelType: "webhook", ConfigEncrypted: []byte("fixture-encrypted")}); e != nil {
		t.Fatal(e)
	}
	out, e := r.IngestMovieUpgradeOrganization(ctx, f.actor, lease.ID)
	if e != nil || out == nil || out.Status != "imported_pending_retirement" || out.ExecutionAllowed || out.JobID != lease.ID {
		t.Fatal(out, e)
	}
	var after string
	if e = f.p.QueryRow(ctx, protected, f.operation).Scan(&after); e != nil || after != before {
		t.Fatal("old state changed", e)
	}
	var valid bool
	e = f.p.QueryRow(ctx, `SELECT f.retired_at IS NULL AND fu.retired_at IS NULL AND fact.stage='imported' AND fact.revoked_at IS NULL AND fact.expires_at IS NULL AND fact.source_task_id IS NULL AND fact.quality_profile=p.step_snapshot->'quality_profile' AND f.root_id=a.root_id AND fact.root_id=a.root_id AND fu.unit_id=a.unit_id FROM movie_upgrade_organization_ingestion i JOIN library_file f ON f.id=i.file_id JOIN library_file_unit fu ON fu.file_id=f.id JOIN unit_fact fact ON fact.id=i.fact_id JOIN movie_upgrade_organization_preparation p ON p.operation_id=i.operation_id JOIN movie_upgrade_authority a ON a.operation_id=i.operation_id WHERE i.job_id=$1`, lease.ID).Scan(&valid)
	if e != nil || !valid {
		t.Fatal(valid, e)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, e := r.IngestMovieUpgradeOrganization(ctx, f.actor, lease.ID)
			mu.Lock()
			defer mu.Unlock()
			if e != nil || got == nil || *got != *out {
				t.Error("recovery changed", got, e)
			}
		}()
	}
	wg.Wait()
	if files.calls.Load() != 1 {
		t.Fatal("repeated hardlink", files.calls.Load())
	}
	for query, want := range map[string]int{`SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`: 1, `SELECT count(*) FROM outbox_delivery WHERE consumer LIKE 'mediaserver.refresh:%'`: 2, `SELECT count(*) FROM notification_batch_member`: 1} {
		var n int
		if e := f.p.QueryRow(ctx, query).Scan(&n); e != nil || n != want {
			t.Fatal("import publication", query, n, want, e)
		}
	}
	f.exec(t, `UPDATE subscription SET state='paused' WHERE id=(SELECT subscription_id FROM movie_upgrade_authority WHERE operation_id=$1)`, f.operation)
	got, e := r.ReadMovieUpgradeOrganizationIngestionReceipt(ctx, f.actor, lease.ID)
	if e != nil || got == nil || *got != *out {
		t.Fatal("historical receipt depends on active projection", got, e)
	}
	token := *f.actor
	token.Scopes = []string{"read"}
	if got, e = r.ReadMovieUpgradeOrganizationIngestionReceipt(ctx, &token, lease.ID); e == nil || got != nil {
		t.Fatal("API token read command receipt", got, e)
	}
}
func TestMovieUpgradeOrganizationIngestionTailPG(t *testing.T) {
	cases := map[string]string{
		"actor":            `UPDATE app_user SET is_active=false WHERE id=NEW.actor_id`,
		"policy":           `UPDATE policy SET name=name||' changed' WHERE id=(SELECT quality_policy_id FROM subscription WHERE id=(SELECT subscription_id FROM movie_upgrade_authority WHERE operation_id=NEW.operation_id))`,
		"old-confirmation": `UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE source_file_id=(SELECT old_file_id FROM movie_upgrade_authority WHERE operation_id=NEW.operation_id)`,
		"root":             `UPDATE library_root SET generation=generation+1 WHERE id=(SELECT root_id FROM movie_upgrade_authority WHERE operation_id=NEW.operation_id)`,
		"new-fact":         `UPDATE unit_fact SET quality_profile='{}' WHERE id=NEW.fact_id`,
		"extra-mapping":    `INSERT INTO library_file_unit(file_id,unit_id) SELECT NEW.file_id,id FROM media_unit WHERE id<>(SELECT unit_id FROM movie_upgrade_authority WHERE operation_id=NEW.operation_id) LIMIT 1`,
	}
	for name, mutation := range cases {
		t.Run(name, func(t *testing.T) {
			f, lease, _, r := ingestionFixture78(t)
			// This row exists before our snapshots; only the tail introduces a foreign mapping.
			if name == "extra-mapping" {
				f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) SELECT gen_random_uuid(),u.media_id,'foreign-ingest-unit' FROM movie_upgrade_authority a JOIN media_unit u ON u.id=a.unit_id WHERE a.operation_id=$1`, f.operation)
			}
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			mutation = strings.NewReplacer("NEW.operation_id", "(NEW.payload->>'operation_id')::uuid", "NEW.actor_id", "NEW.actor_id::uuid", "NEW.file_id", "(NEW.payload->>'file_id')::uuid", "NEW.fact_id", "(NEW.payload->>'fact_id')::uuid").Replace(mutation)
			action := `CREATE FUNCTION ingest78_tail()RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN IF NEW.action='upgrade.organization-ingest' THEN ` + mutation + `;END IF;RETURN NEW;END $$;CREATE TRIGGER ingest78_tail AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION ingest78_tail()`
			f.exec(t, action)
			if got, e := r.IngestMovieUpgradeOrganization(context.Background(), f.actor, lease.ID); e == nil || got != nil {
				t.Fatal("tail accepted", got, e)
			}
			assertIngestionAbsent(t, f)
			if after := nativeProtectedGraph(t, f.preparedUpgradeFixture); after != before {
				t.Fatal("failed transaction leaked changes")
			}
		})
	}
}
func TestMovieUpgradeOrganizationIngestionIdentityPG(t *testing.T) {
	f, lease, _, r := ingestionFixture78(t)
	var root, target string
	if e := f.p.QueryRow(context.Background(), `SELECT snapshot->'physical'->>'root_path',snapshot->'physical'->>'target_rel' FROM movie_upgrade_organization_preparation WHERE operation_id=$1`, f.operation).Scan(&root, &target); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, target)
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("substituted video"), 0600); e != nil {
		t.Fatal(e)
	}
	if got, e := r.IngestMovieUpgradeOrganization(context.Background(), f.actor, lease.ID); e == nil || got != nil {
		t.Fatal("foreign inode imported", got, e)
	}
	assertIngestionAbsent(t, f)
}
func TestMovieUpgradeOrganizationIngestionBusinessRestorePG(t *testing.T) {
	f, lease, files, _ := ingestionFixture78(t)
	ctx := context.Background()
	_, dsn := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	r, e := postgres.NewMovieUpgradeOrganizationIngestionRepository(nativePreparationStore(t, p), files, outbox.New(p, idgen.New(), nativeUpgradeClock{}))
	if e != nil {
		t.Fatal(e)
	}
	out, e := r.IngestMovieUpgradeOrganization(ctx, f.actor, lease.ID)
	if e != nil || out == nil {
		t.Fatal(out, e)
	}
	for _, q := range []string{`ALTER TABLE movie_upgrade_organization_ingestion ADD COLUMN forbidden int`, `TRUNCATE movie_upgrade_organization_ingestion`, `UPDATE movie_upgrade_organization_ingestion SET ingested_at=ingested_at+interval '1 second'`, `DELETE FROM movie_upgrade_organization_ingestion`} {
		if _, e = p.Exec(ctx, q); e == nil {
			t.Fatal("business mutated history", q)
		}
	}
	dump := filepath.Join(t.TempDir(), "ingestion78.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	rp, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer rp.Close()
	rr, e := postgres.NewMovieUpgradeOrganizationIngestionRepository(nativePreparationStore(t, rp), files, outbox.New(rp, idgen.New(), nativeUpgradeClock{}))
	if e != nil {
		t.Fatal(e)
	}
	got, e := rr.IngestMovieUpgradeOrganization(ctx, f.actor, lease.ID)
	if e != nil || got == nil || *got != *out || files.calls.Load() != 1 {
		t.Fatal("restored import repeated", got, e)
	}
	// The frozen history belongs to schema 78; later empty migrations may down.
	rollbackUpgradeTestTo(t, ctx, restored, rp, 78)
	if _, e = postgres.Rollback(ctx, restored, "../../../migrations"); e == nil || !strings.Contains(e.Error(), "upgrade ingestion history prevents downgrade") {
		t.Fatal(e)
	}
}

func TestMovieUpgradeOrganizationIngestionConcurrentPG(t *testing.T) {
	f, lease, files, r := ingestionFixture78(t)
	runtime, e := postgres.NewMovieUpgradeOrganizationRuntimeStore(f.trackingFixture.preparation)
	if e != nil {
		t.Fatal(e)
	}
	pending, e := runtime.PendingMovieUpgradeOrganizationIngestion(context.Background())
	if e != nil || len(pending) != 1 || pending[0].JobID != lease.ID || pending[0].Actor.ID != f.actor.ID {
		t.Fatal(pending, e)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var expected *ports.MovieUpgradeOrganizationIngestionResult
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, e := r.IngestMovieUpgradeOrganization(context.Background(), f.actor, lease.ID)
			mu.Lock()
			defer mu.Unlock()
			if e != nil || got == nil {
				t.Error("concurrent import", got, e)
				return
			}
			if expected == nil {
				expected = got
			} else if *expected != *got {
				t.Error("different committed imports", expected, got)
			}
		}()
	}
	close(start)
	wg.Wait()
	restarted, e := postgres.NewMovieUpgradeOrganizationRuntimeStore(f.trackingFixture.preparation)
	if e != nil {
		t.Fatal(e)
	}
	pending, e = restarted.PendingMovieUpgradeOrganizationIngestion(context.Background())
	if e != nil || len(pending) != 0 || files.calls.Load() != 1 {
		t.Fatal(pending, e, files.calls.Load())
	}
}
func TestMovieUpgradeOrganizationIngestionMigrationPG(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if v, e := postgres.Migrate(ctx, dsn, organization77MigrationDir(t)); e != nil || v != 77 {
		t.Fatal(v, e)
	}
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	var before string
	read := func() string {
		var raw string
		e := p.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('name',proname,'definition',pg_get_functiondef(oid)) ORDER BY proname)::text FROM pg_proc WHERE proname IN ('pulse_upgrade_organization_job_update','pulse_assert_upgrade_organization_claim','pulse_assert_upgrade_organization_preparation','pulse_upgrade_tracking_original_digest')`).Scan(&raw)
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	before = read()
	if v, e := postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil || v < 82 {
		t.Fatal(v, e)
	}
	rollbackUpgradeTestTo(t, ctx, dsn, p, 77)
	if after := read(); after != before {
		t.Fatal("migration changed prior frozen guards")
	}
	if v, e := postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil || v < 82 {
		t.Fatal(v, e)
	}
}

// Walk empty newer migrations before exercising the historical guard under test.
func rollbackUpgradeTestTo(t *testing.T, ctx context.Context, dsn string, pool *pgxpool.Pool, target int64) {
	t.Helper()
	var version int64
	if err := pool.QueryRow(ctx, "SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < target {
		t.Fatalf("schema %d is below historical target %d", version, target)
	}
	for version > target {
		next, err := postgres.Rollback(ctx, dsn, "../../../migrations")
		if err != nil || next != version-1 {
			t.Fatalf("rollback schema %d to %d: got %d: %v", version, version-1, next, err)
		}
		version = next
	}
}
