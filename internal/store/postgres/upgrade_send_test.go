package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preparationMigrationDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join("..", "..", "..", "migrations")
	entries, e := os.ReadDir(source)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		n := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(n, ".sql") || n >= "00066" {
			continue
		}
		b, e := os.ReadFile(filepath.Join(source, n))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, n), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return dir
}
func armFixture(t *testing.T) *preparedUpgradeFixture {
	t.Helper()
	f := preparingUpgrade(t)
	ctx := context.Background()
	if _, e := f.preparation.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "prepare"); e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Migrate(ctx, f.dsn, sendArmMigrationDir(t)); e != nil || v != 66 {
		t.Fatalf("migrate66: %d %v", v, e)
	}
	return f
}
func armSession(t *testing.T, f *preparedUpgradeFixture) ports.MovieUpgradeSendSession {
	t.Helper()
	s, e := f.preparation.OpenMovieUpgradeSendSession(context.Background(), f.operation)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(context.Background()); e != nil {
			t.Error(e)
		}
	})
	return s
}
func noArm(t *testing.T, f *preparedUpgradeFixture) {
	t.Helper()
	var n int
	if e := f.p.QueryRow(context.Background(), `SELECT(SELECT count(*) FROM movie_upgrade_send_authorization)+(SELECT count(*) FROM audit_log WHERE action='upgrade.arm')+(SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.arm:%')`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("partial arm: %d %v", n, e)
	}
}
func TestMovieUpgradeSendArmPG(t *testing.T) {
	f := armFixture(t)
	before := f.oldState(t)
	ctx := context.Background()
	s := armSession(t, f)
	out, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "arm")
	if e != nil {
		t.Fatal(e)
	}
	if out.Status != "armed_not_sent" || out.ExecutionAllowed || out.Token == uuid.Nil {
		t.Fatalf("bad arm: %+v", out)
	}
	if _, e = s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "arm"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatalf("repeat: %v", e)
	}
	if _, e = f.preparation.OpenMovieUpgradeSendSession(ctx, f.operation); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatalf("overlap: %v", e)
	}
	if e = s.Close(ctx); e != nil {
		t.Fatal(e)
	}
	second := armSession(t, f)
	if _, e = second.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "other"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatalf("reentry: %v", e)
	}
	if f.oldState(t) != before {
		t.Fatal("old state changed")
	}
	var valid bool
	e = f.p.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(t.state='submitting' AND to_jsonb(t)=p.task_snapshot) FROM download_task t JOIN movie_upgrade_preparation p ON p.task_id=t.id WHERE t.operation_id=$1`, f.operation).Scan(&valid)
	if e != nil || !valid {
		t.Fatal(valid, e)
	}
}
func TestMovieUpgradeSendArmRejectsPG(t *testing.T) {
	cases := map[string]string{"root": "UPDATE library_root SET generation=generation+1 WHERE id=(SELECT root_id FROM movie_upgrade_authority WHERE operation_id=$1)", "client": "UPDATE download_client SET password_encrypted='changed'::bytea WHERE id=(SELECT client_id FROM movie_upgrade_authority WHERE operation_id=$1)", "fact": "UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE id=(SELECT old_fact_id FROM movie_upgrade_authority WHERE operation_id=$1)"}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			f := armFixture(t)
			s := armSession(t, f)
			f.exec(t, sql, f.operation)
			before := f.oldState(t)
			if _, e := s.ArmMovieUpgradeSend(context.Background(), f.actor, uuid.New(), f.proof, "stale"); e == nil {
				t.Fatal("accepted changed input")
			}
			noArm(t, f)
			if f.oldState(t) != before {
				t.Fatal("old state changed")
			}
		})
	}
}
func TestMovieUpgradeSendArmTailRollbackPG(t *testing.T) {
	for _, stage := range []string{"insert", "audit"} {
		t.Run(stage, func(t *testing.T) {
			f := armFixture(t)
			s := armSession(t, f)
			before := f.oldState(t)
			if stage == "insert" {
				f.exec(t, `CREATE FUNCTION test_arm_corrupt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.proof=jsonb_set(NEW.proof,'{total_bytes}','123'); RETURN NEW; END $$;CREATE TRIGGER test_arm_corrupt BEFORE INSERT ON movie_upgrade_send_authorization FOR EACH ROW EXECUTE FUNCTION test_arm_corrupt()`)
			} else {
				f.exec(t, `CREATE FUNCTION test_arm_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='upgrade.arm' THEN RAISE EXCEPTION 'controlled fault'; END IF;RETURN NEW;END $$;CREATE TRIGGER test_arm_tail BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION test_arm_tail()`)
			}
			if _, e := s.ArmMovieUpgradeSend(context.Background(), f.actor, uuid.New(), f.proof, "fault"); e == nil {
				t.Fatal("accepted fault")
			}
			noArm(t, f)
			if f.oldState(t) != before {
				t.Fatal("old state changed")
			}
		})
	}
}
func TestMovieUpgradeSendArmConnectionLossPG(t *testing.T) {
	f := armFixture(t)
	s := armSession(t, f)
	ctx := context.Background()
	if _, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "arm"); e != nil {
		t.Fatal(e)
	}
	var pid int32
	if e := f.p.QueryRow(ctx, `SELECT backend_pid FROM movie_upgrade_send_authorization WHERE operation_id=$1`, f.operation).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	var terminated bool
	if e := f.p.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); e != nil || !terminated {
		t.Fatal(terminated, e)
	}
	_ = s.Close(ctx)
	other := armSession(t, f)
	if _, e := other.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "after-loss"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatalf("permit lost: %v", e)
	}
}
