package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"testing"
	"time"
)

func waitPreparationBlock(t *testing.T, p *pgxpool.Pool, blocker uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var blocked bool
		if err := p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("preparation did not wait on held row")
}
func shortenPreparationLease(t *testing.T, f *preparedUpgradeFixture) {
	t.Helper()
	// Advance only this private fixture clock. Production still uses a 60s lease.
	f.exec(t, `ALTER TABLE movie_upgrade_preparation_lease DISABLE TRIGGER movie_upgrade_preparation_lease_guard;
 WITH stamp AS (SELECT clock_timestamp() AS now) UPDATE movie_upgrade_preparation_lease SET claimed_at=stamp.now-interval '59 seconds',lease_until=stamp.now+interval '1 second' FROM stamp;
 SET CONSTRAINTS ALL IMMEDIATE;
 ALTER TABLE movie_upgrade_preparation_lease ENABLE TRIGGER movie_upgrade_preparation_lease_guard`)
}
func TestMovieUpgradePreparationLeaseWaitPG(t *testing.T) {
	f := preparingUpgrade(t)
	shortenPreparationLease(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := f.p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackFrozenUpgradeTest(t, tx)
	if _, err = tx.Exec(ctx, `SELECT operation_id FROM movie_upgrade_preparation_lease WHERE operation_id=$1 FOR UPDATE`, f.operation); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := f.preparation.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "lease-wait"); done <- e }()
	waitPreparationBlock(t, f.p, tx.Conn().PgConn().PID())
	if _, err = f.p.Exec(ctx, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM (lease_until-clock_timestamp())))+0.03) FROM movie_upgrade_preparation_lease WHERE operation_id=$1`, f.operation); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ports.ErrUpgradeReservationConflict) {
		t.Fatal(err)
	}
	f.assertUnprepared(t)
}
func TestMovieUpgradePreparationTailLeasePG(t *testing.T) {
	f := preparingUpgrade(t)
	shortenPreparationLease(t, f)
	f.exec(t, `CREATE FUNCTION test_prep_expiry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.idempotency_key LIKE 'upgrade.prepare:%' THEN PERFORM pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM ((SELECT lease_until FROM movie_upgrade_preparation_lease WHERE operation_id='`+f.operation.String()+`')-clock_timestamp())))+0.03); END IF; RETURN NEW; END $$;
 CREATE TRIGGER test_prep_expiry BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION test_prep_expiry()`)
	before := f.oldState(t)
	_, err := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "tail-lease")
	if !errors.Is(err, ports.ErrUpgradeReservationConflict) {
		t.Fatal(err)
	}
	f.assertUnprepared(t)
	if before != f.oldState(t) {
		t.Fatal("expiry changed old file/facts")
	}
}
func TestMovieUpgradePreparationActorWaitPG(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		name := "revoked"
		if cancelCaller {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			f := preparingUpgrade(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := f.p.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackFrozenUpgradeTest(t, tx)
			if _, err = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, e := f.preparation.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "actor-wait"); done <- e }()
			waitPreparationBlock(t, f.p, tx.Conn().PgConn().PID())
			if cancelCaller {
				cancel()
			} else if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if cancelCaller {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ports.ErrForbidden) {
				t.Fatal(err)
			}
			f.assertUnprepared(t)
		})
	}
}

type pretendingPreparationProof struct {
	ports.MovieUpgradeMetainfoProof
}

func (pretendingPreparationProof) ValidateBinding() bool { return true }
func TestMovieUpgradePreparationProofAdmissionPG(t *testing.T) {
	for _, kind := range []string{"wrapped", "typed-nil", "other-target"} {
		t.Run(kind, func(t *testing.T) {
			f := preparingUpgrade(t)
			c := f.prepare()
			switch kind {
			case "wrapped":
				c.Proof = pretendingPreparationProof{f.proof}
			case "typed-nil":
				var p *torrent.MovieUpgradeMetainfoProof
				c.Proof = p
			case "other-target":
				target := f.proof.Target()
				target.MediaID = uuid.New()
				p, e := torrent.InspectMovieUpgradeMetainfo(f.proof.Bytes(), target)
				if e != nil {
					t.Fatal(e)
				}
				c.Proof = p
			}
			_, err := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, c, "bad-proof")
			if !errors.Is(err, ports.ErrUpgradeReservationConflict) {
				t.Fatal(err)
			}
			f.assertUnprepared(t)
		})
	}
}
func TestMovieUpgradePreparationExtraFactPG(t *testing.T) {
	for _, table := range []string{"download_task", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := preparingUpgrade(t)
			f.exec(t, `CREATE FUNCTION test_extra_fact() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE media_unit SET facts_version=facts_version+1 WHERE id='`+f.unit.String()+`'; RETURN NEW; END $$; CREATE TRIGGER test_extra_fact BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION test_extra_fact()`)
			before := f.oldState(t)
			_, err := f.preparation.PrepareMovieUpgrade(context.Background(), f.actor, f.prepare(), "extra-fact")
			if !errors.Is(err, ports.ErrUpgradeReservationConflict) {
				t.Fatal(err)
			}
			f.assertUnprepared(t)
			if before != f.oldState(t) {
				t.Fatal("extra bump committed")
			}
		})
	}
}
func TestMovieUpgradePreparationIsolationPG(t *testing.T) {
	f := preparingUpgrade(t)
	ctx := context.Background()
	if _, err := f.preparation.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "isolated"); err != nil {
		t.Fatal(err)
	}
	m := download.NewManager(f.p, idgen.New(), make([]byte, 32))
	probe := &frozenNoCalls{}
	m.RegisterClient(f.client, probe)
	if _, err := m.PrepareDownload(ctx, f.operation, f.client, f.candidate, "/never-open"); !errors.Is(err, ports.ErrUpgradeNotExecutable) {
		t.Fatal(err)
	}
	if _, err := m.ReconcileTask(ctx, f.operation); !errors.Is(err, ports.ErrUpgradeNotExecutable) {
		t.Fatal(err)
	}
	if _, err := m.ObserveTask(ctx, f.operation); !errors.Is(err, ports.ErrUpgradeNotExecutable) {
		t.Fatal(err)
	}
	if probe.calls != 0 {
		t.Fatal("physical calls", probe.calls)
	}
	for _, sql := range []string{`UPDATE download_task SET state='tracking' WHERE operation_id=$1`, `UPDATE download_task SET save_path='/changed' WHERE operation_id=$1`, `UPDATE movie_upgrade_preparation SET evidence='{}'::jsonb WHERE operation_id=$1`, `DELETE FROM movie_upgrade_preparation WHERE operation_id=$1`, `DELETE FROM movie_upgrade_preparation_lease WHERE operation_id=$1`} {
		if _, err := f.p.Exec(ctx, sql, f.operation); err == nil {
			t.Fatal("prepared mutation accepted")
		}
	}
}
func TestMovieUpgradePreparationHistoryPG(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		name := "lease"
		if prepared {
			name = "prepared"
		}
		t.Run(name, func(t *testing.T) {
			f := preparingUpgrade(t)
			ctx := context.Background()
			if prepared {
				if _, err := f.preparation.PrepareMovieUpgrade(ctx, f.actor, f.prepare(), "history"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := postgres.Rollback(ctx, f.dsn, filepath.Join("..", "..", "..", "migrations")); err == nil {
				t.Fatal("discarded preparation history")
			}
			if err := postgres.CheckSchemaVersion(ctx, f.p, 65); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMovieUpgradePreparationMigrationPG(t *testing.T) {
	f := frozenUpgrade(t)
	ctx := context.Background()
	dir := preparationMigrationDir(t)
	before := f.oldState(t)
	for _, step := range []int{65, 64, 65} {
		var version int64
		var err error
		if step == 64 {
			version, err = postgres.Rollback(ctx, f.dsn, dir)
		} else {
			version, err = postgres.Migrate(ctx, f.dsn, dir)
		}
		if err != nil || version != int64(step) {
			t.Fatal(version, err)
		}
	}
	if before != f.oldState(t) {
		t.Fatal("migration altered existing file/facts")
	}
}
