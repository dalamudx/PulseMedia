package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"testing"
	"time"
)

func TestMovieUpgradeSendReturnTailPG(t *testing.T) {
	for _, kind := range []string{"result", "fact", "receipt", "deferred_fact"} {
		t.Run(kind, func(t *testing.T) {
			f, s, p := returnedFixture(t, "confirmed")
			graph, old := returnGraph(t, f), f.oldState(t)
			switch kind {
			case "result":
				f.exec(t, `CREATE FUNCTION test_result_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN NEW.status:='unknown';RETURN NEW;END$$;CREATE TRIGGER test_result_tail BEFORE INSERT ON movie_upgrade_send_result FOR EACH ROW EXECUTE FUNCTION test_result_tail()`)
			case "fact":
				f.exec(t, `CREATE FUNCTION test_result_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='upgrade.send-result' THEN UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE id=(SELECT old_fact_id FROM movie_upgrade_authority WHERE operation_id=(NEW.payload->>'operation_id')::uuid);END IF;RETURN NEW;END$$;CREATE TRIGGER test_result_tail AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION test_result_tail()`)
			case "receipt":
				f.exec(t, `CREATE FUNCTION test_result_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key LIKE 'upgrade.send-result:%' THEN RAISE EXCEPTION 'result receipt tail';END IF;RETURN NEW;END$$;CREATE TRIGGER test_result_tail BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION test_result_tail()`)
			case "deferred_fact":
				f.exec(t, `CREATE FUNCTION test_result_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key LIKE 'upgrade.send-result:%' THEN UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE id=(SELECT old_fact_id FROM movie_upgrade_authority WHERE operation_id=NEW.resource_id::uuid);END IF;RETURN NEW;END$$;CREATE CONSTRAINT TRIGGER test_result_tail AFTER INSERT ON idempotency_record DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION test_result_tail()`)
			}
			if _, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "tail", p); e == nil {
				t.Fatal("tail accepted")
			}
			noReturn(t, f)
			if returnGraph(t, f) != graph || f.oldState(t) != old {
				t.Fatal("partial result state")
			}
		})
	}
}
func TestMovieUpgradeSendReturnAuthorityWaitPG(t *testing.T) {
	f, s, p := returnedFixture(t, "confirmed")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	go func() { _, e := s.RecordMovieUpgradeSendResult(ctx, f.actor, "revoke-wait", p); done <- e }()
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
		t.Fatal(e)
	}
	noReturn(t, f)
}
func TestMovieUpgradeSendReturnFinalExpiryPG(t *testing.T) {
	f, s, p := returnedFixture(t, "confirmed")
	before, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "expiry", p)
	if e != nil {
		t.Fatal(e)
	}
	var body string
	if e = f.p.QueryRow(context.Background(), `SELECT prosrc FROM pg_proc WHERE oid='pulse_assert_upgrade_preparation'::regproc`).Scan(&body); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION test_original_result_assert(op uuid) RETURNS void LANGUAGE plpgsql AS $test_body$`+body+`$test_body$;CREATE TABLE test_result_checks(n int NOT NULL);INSERT INTO test_result_checks VALUES(0);CREATE OR REPLACE FUNCTION pulse_assert_upgrade_preparation(op uuid) RETURNS void LANGUAGE plpgsql AS $$DECLARE calls int;until_at timestamptz;BEGIN PERFORM test_original_result_assert(op);UPDATE test_result_checks SET n=n+1 RETURNING n INTO calls;IF calls=2 THEN SELECT lease_until INTO until_at FROM movie_upgrade_send_authorization WHERE operation_id=op;PERFORM pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM(until_at-clock_timestamp())))+0.03);END IF;END$$`)
	// Age the actual immutable lease; do not rewrite its snapshot to manufacture expiry.
	if _, e = f.p.Exec(context.Background(), `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM(armed_at+interval '50 seconds'-clock_timestamp())))) FROM movie_upgrade_send_authorization WHERE operation_id=$1`, f.operation); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "expiry", p); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	var n int
	if e = f.p.QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_send_result WHERE operation_id=$1 AND recorded_at=$2`, f.operation, before.RecordedAt).Scan(&n); e != nil || n != 1 {
		t.Fatal("history altered", n, e)
	}
}
func TestMovieUpgradeSendReturnMigrationPG(t *testing.T) {
	f := armFixture(t)
	ctx := context.Background()
	dir := sendResultMigrationDir(t)
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 67 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(ctx, f.dsn, dir); e != nil || v != 66 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 67 {
		t.Fatal(v, e)
	}
	s := armSession(t, f)
	if _, e := s.ArmMovieUpgradeSend(ctx, f.actor, f.lease.Owner, f.proof, "pending-return"); e != nil {
		t.Fatal(e)
	}
	if _, e := postgres.Rollback(ctx, f.dsn, dir); e == nil {
		t.Fatal("unresolved arm history downgraded")
	}
	if e := postgres.CheckSchemaVersion(ctx, f.p, 67); e != nil {
		t.Fatal(e)
	}
}
func TestMovieUpgradeSendReturnBackupPG(t *testing.T) {
	f, s, p := returnedFixture(t, "unknown")
	if _, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "backup", p); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dump := filepath.Join(t.TempDir(), "return.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	pool, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = postgres.CheckSchemaVersion(ctx, pool, 67); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_send_result WHERE status='unknown'`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	for _, query := range []string{`DELETE FROM movie_upgrade_send_result`, `UPDATE movie_upgrade_send_result SET status='confirmed'`, `UPDATE download_task SET state='tracking'`} {
		if _, e = pool.Exec(ctx, query); e == nil {
			t.Fatal("restored immutable graph changed")
		}
	}
	var leak bool
	if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_result r WHERE to_jsonb(r)::text LIKE '%passkey/abcd%')`).Scan(&leak); e != nil || leak {
		t.Fatal("tracker leaked", e)
	}
}
