package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestTVEpisodeUpgradeControlMigrationPG(t *testing.T) {
	t.Run("roundtrip", func(t *testing.T) {
		dsn := pgtest.GetCluster(t).NewTestDB(t)
		history := repositoryMigrationsThrough(t, 139)
		if v, e := postgres.Migrate(t.Context(), dsn, repositoryMigrationsThrough(t, 138)); e != nil || v != 138 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Migrate(t.Context(), dsn, history); e != nil || v != 139 {
			t.Fatal(v, e)
		}
		if _, e := postgres.Rollback(t.Context(), dsn, history); e != nil {
			t.Fatal(e)
		}
		if v, e := postgres.Migrate(t.Context(), dsn, history); e != nil || v != 139 {
			t.Fatal(v, e)
		}
	})
	t.Run("history-prevents-downgrade", func(t *testing.T) {
		f := newDedicatedControl(t, "tv-episode-upgrade")
		raw, e := os.ReadFile("../../../migrations/00139_tv_episode_upgrade_control.sql")
		if e != nil {
			t.Fatal(e)
		}
		_, down, ok := strings.Cut(string(raw), "-- +goose Down")
		if !ok {
			t.Fatal("missing Down")
		}
		tx, e := f.pool.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, e = tx.Exec(t.Context(), down); e == nil {
			t.Fatal("history silently downgraded")
		}
	})
}

func TestTVEpisodeUpgradeControlExpiredPermitPG(t *testing.T) {
	f := newDedicatedControl(t, "tv-episode-upgrade")
	execIntent(t, f.pool, `CREATE FUNCTION mp27_short_lease() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.expires_at=clock_timestamp()+interval '200 milliseconds';RETURN NEW;END $$`)
	execIntent(t, f.pool, `CREATE TRIGGER aa_mp27_short_lease BEFORE INSERT ON download_control_command FOR EACH ROW EXECUTE FUNCTION mp27_short_lease()`)
	hook := func() { execIntent(t, f.pool, `SELECT pg_sleep(0.25)`) }
	f.onSend.Store(&hook)
	got, e := f.apply(t.Context(), "lease-expired", "pause")
	if e != nil || got == nil || got.State != "unknown" || f.sends.Load() != 1 {
		t.Fatal("expired physical result confirmed", got, e)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_return`); n != 1 {
		t.Fatal("physical return lost", n)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`); n != 0 {
		t.Fatal("real return retained physical hold", n)
	}
	if _, e = f.apply(t.Context(), "lease-replacement", "resume"); e == nil {
		t.Fatal("expired permit allowed replacement")
	}
	f.onSend.Store(nil)
	got, e = f.service.Observe(t.Context(), f.actor, f.task.OperationID, "lease-expired")
	if e != nil || got == nil || got.State != "confirmed" || f.sends.Load() != 1 {
		t.Fatal("explicit observation did not recover", got, e)
	}
}

func TestTVEpisodeUpgradeControlDatabaseCommitExpiryPG(t *testing.T) {
	f := newDedicatedControl(t, "tv-episode-upgrade")
	// Expire between the insert guard and the deferred constraint/last permit check.
	execIntent(t, f.pool, `CREATE FUNCTION mp27_delay_authorization() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='download.control.authorized' THEN PERFORM pg_sleep(0.2);END IF;RETURN NEW;END $$`)
	execIntent(t, f.pool, `CREATE FUNCTION mp27_commit_lease() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.expires_at=clock_timestamp()+interval '150 milliseconds';RETURN NEW;END $$`)
	execIntent(t, f.pool, `CREATE TRIGGER aa_mp27_commit_lease BEFORE INSERT ON download_control_command FOR EACH ROW EXECUTE FUNCTION mp27_commit_lease()`)
	execIntent(t, f.pool, `CREATE TRIGGER mp27_delay_authorization BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION mp27_delay_authorization()`)
	if _, e := f.service.Apply(t.Context(), f.actor, f.task.OperationID, "commit-expired", ports.DownloadControlCommand{Action: "pause", Confirmed: true}); e == nil {
		t.Fatal("commit accepted expired permit")
	}
	if f.sends.Load() != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_command`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_authority`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`) != 0 {
		t.Fatal("expired transaction leaked state or sent command")
	}
}

func TestTVEpisodeUpgradeControlOriginalPhysicalGatePG(t *testing.T) {
	f := newDedicatedControl(t, "tv-episode-upgrade")
	connection, e := f.pool.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer connection.Release()
	var held bool
	if e = connection.QueryRow(t.Context(), `SELECT pg_try_advisory_lock(pulse_tv_upgrade_lock($1))`, f.task.OperationID).Scan(&held); e != nil || !held {
		t.Fatal(held, e)
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock(pulse_tv_upgrade_lock($1))`, f.task.OperationID)
	}()
	if _, e = f.apply(t.Context(), "blocked-by-original", "pause"); e == nil {
		t.Fatal("control bypassed original physical lock")
	}
	if f.sends.Load() != 0 || f.reads.Load() != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_command`) != 0 {
		t.Fatal("blocked control invoked native or persisted command")
	}
	if _, e = connection.Exec(t.Context(), `SELECT pg_advisory_unlock(pulse_tv_upgrade_lock($1))`, f.task.OperationID); e != nil {
		t.Fatal(e)
	}
	got, e := f.apply(t.Context(), "after-original-return", "pause")
	if e != nil || got.State != "confirmed" {
		t.Fatal(got, e)
	}
}

func TestTVEpisodeUpgradeControlScanProgressPG(t *testing.T) {
	f := newDedicatedControl(t, "tv-episode-upgrade")
	f.loseACK.Store(true)
	got, e := f.apply(t.Context(), "scan-progress", "pause")
	if e != nil || got.State != "unknown" {
		t.Fatal(got, e)
	}
	execIntent(t, f.pool, `UPDATE library_root SET (scan_token,mutation_seq,updated_at)=(scan_token + 1,mutation_seq + 1,clock_timestamp()) WHERE id=(SELECT root_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`, f.task.OperationID)
	execIntent(t, f.pool, `UPDATE media_unit SET facts_version=facts_version+1,updated_at=clock_timestamp() WHERE id=(SELECT unit_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`, f.task.OperationID)
	got, e = f.service.Observe(t.Context(), f.actor, f.task.OperationID, "scan-progress")
	if e != nil || got.State != "confirmed" || f.sends.Load() != 1 {
		t.Fatal("derived scan progress invalidated original identity or resent command", got, e)
	}
}
