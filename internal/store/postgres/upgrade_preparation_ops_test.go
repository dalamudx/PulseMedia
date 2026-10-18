package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"testing"
	"time"
)

func TestMovieUpgradePreparationBackupPG(t *testing.T) {
	f := preparingUpgrade(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	original, err := f.preparation.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "backup")
	if err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(t.TempDir(), "preparation.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	p, err := pgxpool.New(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err = postgres.CheckSchemaVersion(ctx, p, 65); err != nil {
		t.Fatal(err)
	}
	repo, err := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repo.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "backup")
	if err != nil || *replay != *original {
		t.Fatal("restored receipt differs", err)
	}
	if _, err = p.Exec(ctx, `DELETE FROM movie_upgrade_preparation WHERE operation_id=$1`, f.operation); err == nil {
		t.Fatal("restore lost immutable preparation guard")
	}
}

func TestMovieUpgradePreparationBusinessRolePG(t *testing.T) {
	f := preparingUpgrade(t)
	ctx := context.Background()
	_, dml := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	p, err := pgxpool.New(ctx, dml)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	repo, err := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "business-role")
	if err != nil || result == nil || result.Status != "prepared_not_sent" {
		t.Fatal("business role preparation", err)
	}
	if _, err = p.Exec(ctx, `ALTER TABLE movie_upgrade_preparation ADD COLUMN forbidden integer`); err == nil {
		t.Fatal("business role performed DDL")
	}
}

func TestMovieUpgradePreparationDowngradeConcurrentPG(t *testing.T) {
	f := preparingUpgrade(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	holder, err := f.p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	const lock = 7646501
	if _, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `CREATE FUNCTION test_prep_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(7646501); RETURN NEW; END $$; CREATE TRIGGER test_prep_pause BEFORE INSERT ON movie_upgrade_preparation FOR EACH ROW EXECUTE FUNCTION test_prep_pause()`)
	prepared := make(chan error, 1)
	go func() {
		_, e := f.preparation.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "down-race")
		prepared <- e
	}()
	blocker := holder.Conn().PgConn().PID()
	waitPreparationBlock(t, f.p, blocker)
	var preparingPID uint32
	if err = f.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid))`, blocker).Scan(&preparingPID); err != nil {
		t.Fatal(err)
	}
	down := make(chan error, 1)
	go func() {
		_, e := postgres.Rollback(ctx, f.dsn, filepath.Join("..", "..", "..", "migrations"))
		down <- e
	}()
	waitPreparationBlock(t, f.p, preparingPID)
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-prepared; err != nil {
		t.Fatal("preparation lost race", err)
	}
	if err = <-down; err == nil {
		t.Fatal("downgrade discarded concurrent preparation")
	}
	if err = postgres.CheckSchemaVersion(ctx, f.p, 65); err != nil {
		t.Fatal(err)
	}
	var tasks, proofs int
	if err = f.p.QueryRow(ctx, `SELECT(SELECT count(*) FROM download_task WHERE operation_id=$1),(SELECT count(*) FROM movie_upgrade_preparation WHERE operation_id=$1)`, f.operation).Scan(&tasks, &proofs); err != nil || tasks != 1 || proofs != 1 {
		t.Fatal(tasks, proofs, err)
	}
}
