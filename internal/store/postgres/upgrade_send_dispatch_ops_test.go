package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"testing"
	"time"
)

func TestMovieUpgradeDispatchLeaseExpiryPG(t *testing.T) {
	f, s := dispatchFixture(t)
	ctx := context.Background()
	graph, old := returnGraph(t, f), f.oldState(t)
	var body string
	if e := f.p.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid='pulse_assert_upgrade_preparation'::regproc`).Scan(&body); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION test_original_dispatch_assert(op uuid) RETURNS void LANGUAGE plpgsql AS $test_body$`+body+`$test_body$;CREATE TABLE test_dispatch_assert_calls(n int);INSERT INTO test_dispatch_assert_calls VALUES(0);CREATE OR REPLACE FUNCTION pulse_assert_upgrade_preparation(op uuid) RETURNS void LANGUAGE plpgsql AS $$DECLARE until_at timestamptz;BEGIN PERFORM test_original_dispatch_assert(op);UPDATE test_dispatch_assert_calls SET n=n+1;SELECT lease_until INTO until_at FROM movie_upgrade_send_authorization WHERE operation_id=op;PERFORM pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM(until_at-clock_timestamp())))+0.03);END$$`)
	if _, e := f.p.Exec(ctx, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM(armed_at+interval '50 seconds'-clock_timestamp())))) FROM movie_upgrade_send_authorization WHERE operation_id=$1`, f.operation); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "expired"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal("late dispatch accepted or misclassified", e)
	}
	noDispatch(t, f)
	if graph != returnGraph(t, f) || old != f.oldState(t) {
		t.Fatal("lease expiry left changed graph")
	}
	// Prove the failure did not occur before the real immutable Arm deadline.
	var expired bool
	if e := f.p.QueryRow(ctx, `SELECT lease_until<=clock_timestamp() FROM movie_upgrade_send_authorization WHERE operation_id=$1`, f.operation).Scan(&expired); e != nil || !expired {
		t.Fatal("test never crossed actual lease", e)
	}
}
func TestMovieUpgradeDispatchRevokeWaitPG(t *testing.T) {
	f, s := dispatchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, tx)
	if _, e = tx.Exec(ctx, `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := s.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "revoke"); done <- e }()
	drained := false
	defer func() {
		cancel()
		if !drained {
			<-done
		}
	}()
	waitPreparationBlock(t, f.p, tx.Conn().PgConn().PID())
	if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-done
	drained = true
	if !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revocation bypassed", e)
	}
	noDispatch(t, f)
}
func TestMovieUpgradeDispatchBackupPG(t *testing.T) {
	f, s := dispatchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	receipt, e := s.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "backup")
	if e != nil {
		t.Fatal(e)
	}
	dump := filepath.Join(t.TempDir(), "dispatch.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	pool, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = postgres.CheckSchemaVersion(ctx, pool, 68); e != nil {
		t.Fatal(e)
	}
	var retained, leak bool
	e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_dispatch WHERE operation_id=$1 AND dispatch_id=$2),EXISTS(SELECT 1 FROM movie_upgrade_send_dispatch d WHERE to_jsonb(d)::text LIKE '%passkey/abcd%')`, f.operation, receipt.DispatchID).Scan(&retained, &leak)
	if e != nil || !retained || leak {
		t.Fatal("restore changed history or leaked tracker", e)
	}
	for _, query := range []string{`UPDATE movie_upgrade_send_dispatch SET token=gen_random_uuid()`, `DELETE FROM movie_upgrade_send_dispatch`, `TRUNCATE movie_upgrade_send_dispatch`, `UPDATE download_task SET state='tracking'`} {
		if _, e = pool.Exec(ctx, query); e == nil {
			t.Fatal("restored graph changed", query)
		}
	}
	if _, e = postgres.Rollback(ctx, restored, dispatchMigrationDir(t)); e == nil {
		t.Fatal("restored history downgraded")
	}
}
func TestMovieUpgradeDispatchBusinessRolePG(t *testing.T) {
	f := armFixture(t)
	ctx := context.Background()
	if v, e := postgres.Migrate(ctx, f.dsn, dispatchMigrationDir(t)); e != nil || v != 68 {
		t.Fatal(v, e)
	}
	_, dml := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	pool, e := pgxpool.New(ctx, dml)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	store, e := postgres.NewUpgradePreparationStore(pool, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	repo, e := postgres.NewMovieUpgradeSendDispatchRepository(store)
	if e != nil {
		t.Fatal(e)
	}
	s, e := repo.OpenMovieUpgradeSendDispatchSession(ctx, f.operation)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := s.Close(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	if _, e = s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "dml-arm"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "dml"); e != nil {
		t.Fatal(e)
	}
	for _, query := range []string{`ALTER TABLE movie_upgrade_send_dispatch ADD COLUMN forbidden INT`, `DELETE FROM movie_upgrade_send_dispatch`, `UPDATE movie_upgrade_send_dispatch SET token=gen_random_uuid()`, `TRUNCATE movie_upgrade_send_dispatch`} {
		if _, e = pool.Exec(ctx, query); e == nil {
			t.Fatal("DML exceeded its rights", query)
		}
	}
}
func TestMovieUpgradeDispatchMigrationPG(t *testing.T) {
	f := armFixture(t)
	ctx := context.Background()
	dir := dispatchMigrationDir(t)
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 68 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(ctx, f.dsn, dir); e != nil || v != 67 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 68 {
		t.Fatal(v, e)
	}
}
func TestMovieUpgradeDispatchDownConcurrentPG(t *testing.T) {
	f, s := dispatchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	holder, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(680,1)`); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION test_dispatch_down_wait() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='upgrade.dispatch' THEN PERFORM pg_advisory_xact_lock(680,1);END IF;RETURN NEW;END$$;CREATE TRIGGER test_dispatch_down_wait AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION test_dispatch_down_wait()`)
	sent, down := make(chan error, 1), make(chan error, 1)
	drained, started, downDrained := false, false, false
	defer func() {
		cancel()
		if !drained {
			<-sent
		}
		if started && !downDrained {
			<-down
		}
	}()
	go func() { _, e := s.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "concurrent-down"); sent <- e }()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	var pid uint32
	if e = f.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1::INTEGER=ANY(pg_blocking_pids(pid))`, holder.Conn().PgConn().PID()).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	started = true
	go func() {
		_, e := postgres.Rollback(ctx, f.dsn, dispatchMigrationDir(t))
		down <- e
	}()
	waitPreparationBlock(t, f.p, pid)
	if e = holder.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-sent
	drained = true
	if e != nil {
		t.Fatal(e)
	}
	e = <-down
	downDrained = true
	if e == nil {
		t.Fatal("concurrent downgrade erased dispatch")
	}
	if e = postgres.CheckSchemaVersion(ctx, f.p, 68); e != nil {
		t.Fatal(e)
	}
}
