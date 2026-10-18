package postgres_test

import (
	"bytes"
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

func TestMovieUpgradeSendArmActorWaitPG(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "canceled"}[canceled], func(t *testing.T) {
			f := armFixture(t)
			s := armSession(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			holder, e := f.p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer rollbackFrozenUpgradeTest(t, holder)
			if _, e = holder.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			drained := false
			go func() { _, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "actor-wait"); done <- e }()
			defer func() {
				cancel()
				if !drained {
					<-done
				}
			}()
			waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
			if canceled {
				cancel()
			} else if e = holder.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			drained = true
			if canceled {
				if !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
			} else if !errors.Is(e, ports.ErrForbidden) {
				t.Fatal(e)
			}
			noArm(t, f)
		})
	}
}
func TestMovieUpgradeSendArmCloseBoundPG(t *testing.T) {
	f := armFixture(t)
	s := armSession(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	holder, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	if _, e = holder.Exec(ctx, `UPDATE app_user SET is_active=true WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	done, closed := make(chan error, 1), make(chan error, 1)
	drained, closeDrained, closeStarted := false, false, false
	go func() { _, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "close-wait"); done <- e }()
	defer func() {
		cancel()
		if !drained {
			<-done
		}
		if closeStarted && !closeDrained {
			<-closed
		}
	}()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	c, stop := context.WithCancel(context.Background())
	stop()
	closeStarted = true
	go func() { closed <- s.Close(c) }()
	select {
	case e = <-closed:
		closeDrained = true
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal(e)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("Close exceeded its independent gate/close budget")
	}
	select {
	case e = <-done:
		drained = true
		t.Fatal("Close interrupted active owner", e)
	default:
	}
	if e = holder.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-done
	drained = true
	if e != nil {
		t.Fatal(e)
	}
}
func TestMovieUpgradeSendArmProofPG(t *testing.T) {
	for _, kind := range []string{"wrapped", "typed_nil", "other_bytes"} {
		t.Run(kind, func(t *testing.T) {
			f := armFixture(t)
			s := armSession(t, f)
			var proof ports.MovieUpgradeMetainfoProof = f.proof
			switch kind {
			case "wrapped":
				proof = pretendingPreparationProof{f.proof}
			case "typed_nil":
				var p *torrent.MovieUpgradeMetainfoProof
				proof = p
			case "other_bytes":
				raw := bytes.Replace(f.proof.Bytes(), []byte("passkey/abcd"), []byte("passkey/abce"), 1)
				p, e := torrent.InspectMovieUpgradeMetainfo(raw, f.proof.Target())
				if e != nil {
					t.Fatal(e)
				}
				if p.InfoHash() != f.proof.InfoHash() {
					t.Fatal("fixture changed original info")
				}
				proof = p
			}
			if _, e := s.ArmMovieUpgradeSend(context.Background(), f.actor, uuid.New(), proof, "bad-proof"); e == nil {
				t.Fatal("accepted invalid/rebound/full-byte-different proof")
			}
			noArm(t, f)
		})
	}
}
func TestMovieUpgradeSendArmBackupPG(t *testing.T) {
	f := armFixture(t)
	s := armSession(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "backup"); e != nil {
		t.Fatal(e)
	}
	dump := filepath.Join(t.TempDir(), "arm.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	p, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = postgres.CheckSchemaVersion(ctx, p, 66); e != nil {
		t.Fatal(e)
	}
	repo, e := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	other, e := repo.OpenMovieUpgradeSendSession(ctx, f.operation)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := other.Close(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	if _, e = other.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "restore"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `DELETE FROM movie_upgrade_send_authorization WHERE operation_id=$1`, f.operation); e == nil {
		t.Fatal("restore lost immutable permit")
	}
	var leaked bool
	if e = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_authorization x WHERE to_jsonb(x)::text LIKE '%private-passkey%')`).Scan(&leaked); e != nil || leaked {
		t.Fatal("raw tracker stored", e)
	}
}
func TestMovieUpgradeSendArmBusinessRolePG(t *testing.T) {
	f := armFixture(t)
	ctx := context.Background()
	_, dml := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	p, e := pgxpool.New(ctx, dml)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	repo, e := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
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
	if _, e = s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "business"); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `ALTER TABLE movie_upgrade_send_authorization ADD COLUMN forbidden INTEGER`); e == nil {
		t.Fatal("DML performed DDL")
	}
}
func TestMovieUpgradeSendArmMigrationPG(t *testing.T) {
	f := armFixture(t)
	ctx := context.Background()
	dir := sendArmMigrationDir(t)
	before := f.oldState(t)
	if v, e := postgres.Rollback(ctx, f.dsn, dir); e != nil || v != 65 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 66 {
		t.Fatal(v, e)
	}
	s := armSession(t, f)
	if _, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "history"); e != nil {
		t.Fatal(e)
	}
	if _, e := postgres.Rollback(ctx, f.dsn, dir); e == nil {
		t.Fatal("Down discarded arm history")
	}
	if e := postgres.CheckSchemaVersion(ctx, f.p, 66); e != nil {
		t.Fatal(e)
	}
	if f.oldState(t) != before {
		t.Fatal("migration changed old state")
	}
}
func TestMovieUpgradeSendArmDownConcurrentPG(t *testing.T) {
	f := armFixture(t)
	s := armSession(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	holder, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(766001)`); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION test_arm_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(766001);RETURN NEW;END $$;CREATE TRIGGER test_arm_pause BEFORE INSERT ON movie_upgrade_send_authorization FOR EACH ROW EXECUTE FUNCTION test_arm_pause()`)
	armed, down := make(chan error, 1), make(chan error, 1)
	armedDrained, downDrained, downStarted := false, false, false
	defer func() {
		cancel()
		if !armedDrained {
			<-armed
		}
		if downStarted && !downDrained {
			<-down
		}
	}()
	go func() { _, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "down-race"); armed <- e }()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	var pid uint32
	if e = f.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1::INTEGER=ANY(pg_blocking_pids(pid))`, holder.Conn().PgConn().PID()).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	downStarted = true
	go func() {
		_, e := postgres.Rollback(ctx, f.dsn, sendArmMigrationDir(t))
		down <- e
	}()
	waitPreparationBlock(t, f.p, pid)
	if e = holder.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-armed
	armedDrained = true
	if e != nil {
		t.Fatal(e)
	}
	e = <-down
	downDrained = true
	if e == nil {
		t.Fatal("Down discarded concurrent authorization")
	}
	if e = postgres.CheckSchemaVersion(ctx, f.p, 66); e != nil {
		t.Fatal(e)
	}
}
