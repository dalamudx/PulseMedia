package transfer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func verifiedOpsPGCommand(t *testing.T, dsn, name string, args ...string) {
	t.Helper()
	bin := os.Getenv("PULSE_TEST_PG_BIN")
	if bin == "" {
		t.Fatal("PULSE_TEST_PG_BIN is required for pinned backup tools")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(bin, name), append([]string{"--dbname", dsn}, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PG") {
			command.Env = append(command.Env, entry)
		}
	}
	passfile := filepath.Join(t.TempDir(), "empty-pgpass")
	if err := os.WriteFile(passfile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	command.Env = append(command.Env, "PGPASSFILE="+passfile)
	command.WaitDelay = time.Second
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v: %s", name, err, output)
	}
}

func TestVerifiedActionDMLBackupAndMigrationPG(t *testing.T) {
	f, plan, step, _ := verifiedFixture(t, "move")
	ctx := t.Context()
	cluster := pgtest.GetCluster(t)
	_, dmlDSN := cluster.CreateRoles(t, f.pool.Config().ConnConfig.Database)
	pool, err := pgxpool.New(ctx, dmlDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	gen := idgen.New()
	f.svc = NewService(pool, gen, outbox.New(pool, gen, clock.New()), rootfs.NewFilesystem())
	if err = f.svc.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatal("DML execution", err)
	}
	checkVerifiedImport(t, f, plan, step, "move")
	for _, statement := range []string{
		"ALTER TABLE transfer_content_evidence ADD COLUMN forbidden int",
		"TRUNCATE transfer_content_evidence",
		"UPDATE transfer_content_evidence SET content='{}'::jsonb",
		"DELETE FROM transfer_content_evidence",
	} {
		if _, err = pool.Exec(ctx, statement); err == nil {
			t.Fatal("DML rewrote content history", statement)
		}
	}
	before, err := f.q.GetVerifiedTransferContent(ctx, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(t.TempDir(), "verified-action81.dump")
	verifiedOpsPGCommand(t, f.pool.Config().ConnConfig.ConnString(), "pg_dump", "--no-password", "-Fc", "-f", dump)
	restored := cluster.NewTestDB(t)
	verifiedOpsPGCommand(t, restored, "pg_restore", "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", restored, dump)
	restoredPool, err := pgxpool.New(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restoredPool.Close)
	restoredGen := idgen.New()
	restoredService := NewService(restoredPool, restoredGen, outbox.New(restoredPool, restoredGen, clock.New()), rootfs.NewFilesystem())
	after, err := postgres.Queries(restoredPool).GetVerifiedTransferContent(ctx, step.ID)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("restored content evidence changed", err)
	}
	if err = restoredService.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatal("restored replay", err)
	}
	rf := *f
	rf.pool, rf.q, rf.svc = restoredPool, postgres.Queries(restoredPool), restoredService
	checkVerifiedImport(t, &rf, plan, step, "move")
	// Empty later migrations may be removed; verify the original schema81 guard.
	for {
		var version int64
		versionErr := restoredPool.QueryRow(ctx, "SELECT version_id FROM goose_db_version WHERE is_applied = true ORDER BY id DESC LIMIT 1").Scan(&version)
		if versionErr != nil || version < 81 {
			t.Fatal("unexpected history version", version, versionErr)
		}
		if version == 81 {
			break
		}
		if _, err = postgres.Rollback(ctx, restored, "../../../migrations"); err != nil {
			t.Fatal("empty later migration", err)
		}
	}
	if _, err = postgres.Rollback(ctx, restored, "../../../migrations"); err == nil || !strings.Contains(err.Error(), "verified transfer history prevents downgrade") {
		t.Fatal("verified history lost", err)
	}
}
