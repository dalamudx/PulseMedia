package transfer

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLibraryReuseDMLBackupAndMigrationPG(t *testing.T) {
	f, r, actor, target, source := reusePreviewFixture(t)
	ctx := t.Context()
	command := reuseCommand(t, r, actor, target, source, "copy")
	cluster := pgtest.GetCluster(t)
	_, dmlDSN := cluster.CreateRoles(t, f.pool.Config().ConnConfig.Database)
	pool, err := pgxpool.New(ctx, dmlDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	gen := idgen.New()
	r = NewRepository(pool, gen, outbox.New(pool, gen, clock.New()), rootfs.NewFilesystem())
	business := *f
	business.pool, business.q = pool, postgres.Queries(pool)
	receipt, err := r.ConfirmLibraryReuse(ctx, actor, command, "reuse-dml-original")
	if err != nil {
		t.Fatal("DML confirmation", err)
	}
	if err = r.ExecuteLibraryReuse(ctx, claimReuse(t, &business)); err != nil {
		t.Fatal("DML execution", err)
	}
	before, err := business.q.GetLibraryReuseContent(ctx, receipt.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := business.q.GetLibraryReusePlan(ctx, receipt.PlanID)
	if err != nil || plan.State != "completed" || !plan.TargetFileID.Valid {
		t.Fatal("DML import incomplete", plan, err)
	}
	facts, mappings := business.count(t, "unit_fact"), business.count(t, "library_file_unit")
	for _, statement := range []string{
		"ALTER TABLE library_reuse_plan ADD COLUMN forbidden int",
		"TRUNCATE library_reuse_content",
		"UPDATE library_reuse_content SET content='{}'::jsonb",
		"DELETE FROM library_reuse_plan",
		"DELETE FROM library_reuse_unit",
		"UPDATE library_reuse_unit SET active=true",
		"UPDATE library_reuse_plan SET target_rel='forbidden.mkv'",
	} {
		if _, err = pool.Exec(ctx, statement); err == nil {
			t.Fatal("DML changed immutable reuse history", statement)
		}
	}
	dump := filepath.Join(t.TempDir(), "library-reuse82.dump")
	verifiedOpsPGCommand(t, f.pool.Config().ConnConfig.ConnString(), "pg_dump", "--no-password", "-Fc", "-f", dump)
	restored := cluster.NewTestDB(t)
	verifiedOpsPGCommand(t, restored, "pg_restore", "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", restored, dump)
	restoredPool, err := pgxpool.New(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restoredPool.Close)
	restoredGen := idgen.New()
	recovered := NewRepository(restoredPool, restoredGen, outbox.New(restoredPool, restoredGen, clock.New()), rootfs.NewFilesystem())
	after, err := postgres.Queries(restoredPool).GetLibraryReuseContent(ctx, receipt.PlanID)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("restored content differs", err)
	}
	replay, err := recovered.ConfirmLibraryReuse(ctx, actor, command, "reuse-dml-original")
	if err != nil || replay == nil || *replay != *receipt {
		t.Fatal("restored original receipt", replay, err)
	}
	snapshot := business
	snapshot.pool, snapshot.q = restoredPool, postgres.Queries(restoredPool)
	if snapshot.count(t, "unit_fact") != facts || snapshot.count(t, "library_file_unit") != mappings || snapshot.count(t, "library_reuse_plan") != 1 {
		t.Fatal("restored replay duplicated import")
	}
	for {
		var version int64
		if err = restoredPool.QueryRow(ctx, "SELECT version_id FROM goose_db_version WHERE is_applied=true ORDER BY id DESC LIMIT 1").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version <= 82 {
			break
		}
		if _, err = postgres.Rollback(ctx, restored, "../../../migrations"); err != nil {
			t.Fatal("empty later migration", err)
		}
	}
	if _, err = postgres.Rollback(ctx, restored, "../../../migrations"); err == nil || !strings.Contains(err.Error(), "local reuse history prevents downgrade") {
		t.Fatal("reuse history lost", err)
	}
}

func TestLibraryReuseEmptyMigrationRoundTripPG(t *testing.T) {
	f, _, _, _, _ := reusePreviewFixture(t)
	ctx := t.Context()
	dsn := f.pool.Config().ConnConfig.ConnString()
	var latest int64
	if err := f.pool.QueryRow(ctx, "SELECT version_id FROM goose_db_version WHERE is_applied=true ORDER BY id DESC LIMIT 1").Scan(&latest); err != nil {
		t.Fatal(err)
	}
	for version := latest; version > 82; {
		var err error
		version, err = postgres.Rollback(ctx, dsn, "../../../migrations")
		if err != nil {
			t.Fatal("empty later migration", err)
		}
	}
	version, err := postgres.Rollback(ctx, dsn, "../../../migrations")
	if err != nil || version != 81 {
		t.Fatal("empty schema82 downgrade", version, err)
	}
	version, err = postgres.Migrate(ctx, dsn, "../../../migrations")
	if err != nil || version != latest {
		t.Fatal("schema82 reapply", version, err)
	}
}
