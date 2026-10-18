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
	"testing"
	"time"
)

func TestMovieUpgradeSendReturnProtectionPG(t *testing.T) {
	f, s, p := returnedFixture(t, "confirmed")
	old, graph := f.oldState(t), returnGraph(t, f)
	f.exec(t, `CREATE FUNCTION test_result_fence_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='upgrade.send-result' THEN UPDATE unit_reservation SET updated_at=updated_at+interval '1 second' WHERE operation_id=(NEW.payload->>'operation_id')::uuid;END IF;RETURN NEW;END$$;CREATE TRIGGER test_result_fence_tail AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION test_result_fence_tail()`)
	if _, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "fence-tail", p); e == nil {
		t.Fatal("protection changed")
	}
	noReturn(t, f)
	if graph != returnGraph(t, f) || old != f.oldState(t) {
		t.Fatal("tail rollback incomplete")
	}
}
func TestMovieUpgradeSendReturnBusinessRolePG(t *testing.T) {
	f := armFixture(t)
	ctx := context.Background()
	if v, e := postgres.Migrate(ctx, f.dsn, sendResultMigrationDir(t)); e != nil || v != 67 {
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
	repo, e := postgres.NewMovieUpgradeSendResultRepository(store, testSendReturnVerifier{})
	if e != nil {
		t.Fatal(e)
	}
	s, e := repo.OpenMovieUpgradeSendSession(ctx, f.operation)
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
	p := &testSendReturnProof{}
	p.v.Status = "confirmed"
	b := &p.v.Binding
	if e = f.p.QueryRow(ctx, `SELECT a.operation_id,a.task_id,a.owner,a.token,encode(sha256(convert_to(to_jsonb(a)::text,'UTF8')),'hex'),encode(p.raw_sha256,'hex'),clock_timestamp() FROM movie_upgrade_send_authorization a JOIN movie_upgrade_preparation p ON p.operation_id=a.operation_id WHERE a.operation_id=$1`, f.operation).Scan(&b.OperationID, &b.TaskID, &b.Owner, &b.Token, &b.ArmSHA256, &b.RawSHA256, &p.v.ReturnedAt); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RecordMovieUpgradeSendResult(ctx, f.actor, "dml-result", p); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `ALTER TABLE movie_upgrade_send_result ADD COLUMN forbidden INT`); e == nil {
		t.Fatal("DML performed DDL")
	}
	if _, e = pool.Exec(ctx, `DELETE FROM movie_upgrade_send_authorization`); e == nil {
		t.Fatal("permit released")
	}
}
func TestMovieUpgradeSendReturnDownConcurrentPG(t *testing.T) {
	f, s, p := returnedFixture(t, "confirmed")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	holder, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(767001)`); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION test_result_pause() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(767001);RETURN NEW;END$$;CREATE TRIGGER test_result_pause BEFORE INSERT ON movie_upgrade_send_result FOR EACH ROW EXECUTE FUNCTION test_result_pause()`)
	returned, down := make(chan error, 1), make(chan error, 1)
	returnDrained, downDrained, started := false, false, false
	defer func() {
		cancel()
		if !returnDrained {
			<-returned
		}
		if started && !downDrained {
			<-down
		}
	}()
	go func() { _, e := s.RecordMovieUpgradeSendResult(ctx, f.actor, "down-race", p); returned <- e }()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	var pid uint32
	if e = f.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1::INTEGER=ANY(pg_blocking_pids(pid))`, holder.Conn().PgConn().PID()).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	started = true
	go func() {
		_, e := postgres.Rollback(ctx, f.dsn, sendResultMigrationDir(t))
		down <- e
	}()
	waitPreparationBlock(t, f.p, pid)
	if e = holder.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-returned
	returnDrained = true
	if e != nil {
		t.Fatal(e)
	}
	e = <-down
	downDrained = true
	if e == nil {
		t.Fatal("concurrent Down erased history")
	}
	if e = postgres.CheckSchemaVersion(ctx, f.p, 67); e != nil {
		t.Fatal(e)
	}
}
func TestMovieUpgradeSendReturnNativeSessionLossPG(t *testing.T) {
	f, s, p := returnedFixture(t, "unknown")
	var pid uint32
	if e := f.p.QueryRow(context.Background(), `SELECT backend_pid FROM movie_upgrade_send_authorization WHERE operation_id=$1`, f.operation).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `SELECT pg_terminate_backend($1::int)`, pid)
	if _, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "lost", p); e == nil {
		t.Fatal("lost session wrote result")
	}
	noReturn(t, f)
	other, e := f.preparation.OpenMovieUpgradeSendSession(context.Background(), f.operation)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := other.Close(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	if _, e = other.ArmMovieUpgradeSend(context.Background(), f.actor, uuid.New(), f.proof, "no-replay"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatal(e)
	}
}
