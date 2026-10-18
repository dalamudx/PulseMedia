package postgres_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMovieUpgradeObservationJobMigrationRoundTrip(t *testing.T) {
	n := r3Native(t, "ambiguous", true)
	ctx := context.Background()
	functions := func() string {
		t.Helper()
		var value string
		if err := n.p.QueryRow(ctx, `SELECT pg_get_functiondef('pulse_assert_upgrade_preparation(uuid)'::regprocedure)||pg_get_functiondef('pulse_upgrade_preparation_no_job()'::regprocedure)`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	original := functions()
	dir := observation70MigrationDir(t)
	if version, err := postgres.Migrate(ctx, n.dsn, dir); err != nil || version != 70 {
		t.Fatal(version, err)
	}
	if version, err := postgres.Rollback(ctx, n.dsn, dir); err != nil || version != 69 {
		t.Fatal(version, err)
	}
	if functions() != original {
		t.Fatal("Down did not restore both original schema65 guards")
	}
	var task uuid.UUID
	if err := n.p.QueryRow(ctx, `SELECT task_id FROM movie_upgrade_preparation WHERE operation_id=$1`, n.operation).Scan(&task); err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewQueue(n.p, observationJobIDs{}, nativeUpgradeClock{})
	_, err := queue.Enqueue(ctx, ports.MovieUpgradeObservationJobKind, "movie-upgrade-observation:"+n.operation.String(), 0, map[string]any{"operation_id": n.operation, "task_id": task, "actor_id": n.actor.ID}, time.Time{}, 1)
	if err == nil || !strings.Contains(err.Error(), "upgrade preparation cannot create jobs") {
		t.Fatal("schema69 execution guard was not restored", err)
	}
	if version, err := postgres.Migrate(ctx, n.dsn, dir); err != nil || version != 70 {
		t.Fatal(version, err)
	}
}

func TestMovieUpgradeObservationJobBusinessRoleAndRestore(t *testing.T) {
	n := r3JobNative(t)
	f := newNativeObservation(t, n, false)
	req, _ := claimedObservationJob(t, n)
	ctx := context.Background()
	_, dml := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, n.dsn))
	pool, err := pgxpool.New(ctx, dml)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := postgres.NewUpgradePreparationStore(pool, idgen.New(), torrent.Inspector{})
	if err != nil {
		t.Fatal(err)
	}
	verifier := postgres.NewMovieUpgradeNativeQueryVerifier()
	driver, err := download.NewMovieUpgradeNativeObservationDriver(n.manager, verifier, nativeUpgradeClock{})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := postgres.NewMovieUpgradeNativeJobObservationRepository(store, driver, verifier)
	if err != nil {
		t.Fatal(err)
	}
	out, err := repo.ObserveMovieUpgradeJob(ctx, n.actor, req)
	if err != nil || out == nil || out.Disposition != "manual_review" || out.ExecutionAllowed {
		t.Fatalf("business role execution: %v", err)
	}
	if f.queries.Load() != 1 || n.adds.Load() != 1 {
		t.Fatal("unexpected physical I/O")
	}
	for _, statement := range []string{`ALTER TABLE movie_upgrade_observation_job ADD COLUMN denied int`, `TRUNCATE movie_upgrade_observation_job`, `UPDATE movie_upgrade_observation_job SET result_key='changed'`, `DELETE FROM movie_upgrade_observation_job`} {
		if _, err = pool.Exec(ctx, statement); err == nil {
			t.Fatal("business role changed evidence or DDL", statement)
		}
	}
	dump := filepath.Join(t.TempDir(), "upgrade-job70.dump")
	frozenPGCommand(t, ctx, "pg_dump", n.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	p, err := pgxpool.New(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err = postgres.CheckSchemaVersion(ctx, p, 70); err != nil {
		t.Fatal(err)
	}
	var intact, leak bool
	err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_observation_job b JOIN job j ON j.id=b.job_id WHERE b.job_id=$1 AND to_jsonb(j)=pulse_upgrade_observation_terminal_job(b.observation_id)),EXISTS(SELECT 1 FROM movie_upgrade_observation_job b WHERE to_jsonb(b)::text LIKE '%passkey/abcd%')`, req.Lease.ID).Scan(&intact, &leak)
	if err != nil || !intact || leak {
		t.Fatal("restored job graph mismatch or unsafe evidence", err)
	}
	if _, err = p.Exec(ctx, `TRUNCATE movie_upgrade_observation_job`); err == nil {
		t.Fatal("restored binding mutable")
	}
	if _, err = postgres.Rollback(ctx, restored, observation70MigrationDir(t)); err == nil || !strings.Contains(err.Error(), "cannot downgrade upgrade observation job history") {
		t.Fatal("retained job history guard", err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO job(id,kind,state,payload,max_attempts) SELECT gen_random_uuid(),'download_submit','queued',jsonb_build_object('operation_id',operation_id),1 FROM movie_upgrade_observation_job`); err == nil || !strings.Contains(err.Error(), "upgrade preparation cannot create jobs") {
		t.Fatal("generic download job bypass", err)
	}
}

func TestMovieUpgradeObservationJobDownConcurrent(t *testing.T) {
	n := r3JobNative(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	holder, err := n.p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	_, err = holder.Exec(ctx, `INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts) SELECT gen_random_uuid(),'movie_upgrade_observation','movie-upgrade-observation:'||operation_id::text,'queued',jsonb_build_object('operation_id',operation_id,'task_id',task_id,'actor_id',actor_id),1 FROM movie_upgrade_preparation WHERE operation_id=$1`, n.operation)
	if err != nil {
		t.Fatal(err)
	}
	down := make(chan error, 1)
	drained := false
	defer func() {
		cancel()
		if err := holder.Rollback(context.Background()); err != nil && err.Error() != "tx is closed" {
			t.Error(err)
		}
		if !drained {
			<-down
		}
	}()
	go func() {
		_, err := postgres.Rollback(ctx, n.dsn, observation70MigrationDir(t))
		down <- err
	}()
	waitPreparationBlock(t, n.p, holder.Conn().PgConn().PID())
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-down
	drained = true
	if err == nil || !strings.Contains(err.Error(), "cannot downgrade upgrade observation job history") {
		t.Fatal("concurrent retained job guard", err)
	}
	if err = postgres.CheckSchemaVersion(ctx, n.p, 70); err != nil {
		t.Fatal(err)
	}
}
