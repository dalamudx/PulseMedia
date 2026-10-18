package postgres_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func trackingRepositoryForPool(t *testing.T, f *trackingFixture, pool *pgxpool.Pool) ports.MovieUpgradeTrackingRepository {
	t.Helper()
	store, e := postgres.NewUpgradePreparationStore(pool, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	issuer := postgres.NewMovieUpgradeTrackingQueryVerifier()
	driver, e := download.NewMovieUpgradeNativeTrackingDriver(f.manager, issuer, torrent.Inspector{}, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	repo, e := postgres.NewMovieUpgradeNativeTrackingRepository(store, driver, issuer)
	if e != nil {
		t.Fatal(e)
	}
	return repo
}

func TestMovieUpgradeTrackingMigrationRoundTripPG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	ctx := context.Background()
	dir := tracking72MigrationDir(t)
	function := func() string {
		t.Helper()
		var s string
		if e := f.p.QueryRow(ctx, `SELECT pg_get_functiondef('pulse_upgrade_observation_original_digest(uuid)'::regprocedure)||pg_get_functiondef('pulse_assert_upgrade_preparation(uuid)'::regprocedure)`).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	before := function()
	if v, e := postgres.Rollback(ctx, f.dsn, dir); e != nil || v != 71 {
		t.Fatal(v, e)
	}
	if function() != before {
		t.Fatal("tracking Down rewrote accepted original functions")
	}
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 72 {
		t.Fatal(v, e)
	}
	if function() != before {
		t.Fatal("tracking Up rewrote accepted original functions")
	}
	if out, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "roundtrip-admit", "roundtrip-result"); e != nil || out.Outcome != "completed" {
		t.Fatal(out, e)
	}
}

func TestMovieUpgradeTrackingBusinessRoleRestorePG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	ctx := context.Background()
	_, dsn := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	repo := trackingRepositoryForPool(t, f, pool)
	out, e := repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "business-track-admit", "business-track-result")
	if e != nil || out.Outcome != "completed" {
		t.Fatal(out, e)
	}
	for _, sql := range []string{`ALTER TABLE movie_upgrade_tracking_permit ADD COLUMN forbidden int`, `TRUNCATE movie_upgrade_tracking_observation`, `TRUNCATE movie_upgrade_tracking_permit`, `TRUNCATE movie_upgrade_tracking_result`, `UPDATE movie_upgrade_tracking_observation SET owner=gen_random_uuid()`, `DELETE FROM movie_upgrade_tracking_permit`, `UPDATE movie_upgrade_tracking_result SET disposition='tracking'`} {
		if _, e = pool.Exec(ctx, sql); e == nil {
			t.Fatal("business role mutated tracking evidence or DDL", sql)
		}
	}
	dump := filepath.Join(t.TempDir(), "upgrade-tracking72.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	p, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = postgres.CheckSchemaVersion(ctx, p, 72); e != nil {
		t.Fatal(e)
	}
	restoredRepo := trackingRepositoryForPool(t, f, p)
	calls := f.infos.Load()
	replay, e := restoredRepo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "business-track-admit", "business-track-result")
	if e != nil || *replay != *out || f.infos.Load() != calls {
		t.Fatal("restored historical receipt reissued native query", replay, e)
	}
	var leak bool
	if e = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation o WHERE to_jsonb(o)::text LIKE '%passkey/%') OR EXISTS(SELECT 1 FROM movie_upgrade_tracking_result r WHERE to_jsonb(r)::text LIKE '%passkey/%')`).Scan(&leak); e != nil || leak {
		t.Fatal("tracking retained private metainfo", e)
	}
	if _, e = postgres.Rollback(ctx, restored, filepath.Join("..", "..", "..", "migrations")); e == nil || !strings.Contains(e.Error(), "upgrade tracking history prevents downgrade") {
		t.Fatal("tracking history downgraded", e)
	}
	if e = postgres.CheckSchemaVersion(ctx, p, 72); e != nil {
		t.Fatal(e)
	}
}

func TestMovieUpgradeTrackingDownConcurrentPG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	holder, e := f.p.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Release()
	const key int64 = 174885012711
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
		t.Fatal(e)
	}
	defer func() {
		release, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = holder.Exec(release, `SELECT pg_advisory_unlock($1)`, key)
	}()
	if _, e = f.p.Exec(ctx, `CREATE FUNCTION pulse_test_tracking_gate() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(174885012711);RETURN NEW;END$$;CREATE TRIGGER pulse_test_tracking_gate AFTER INSERT ON movie_upgrade_tracking_observation FOR EACH ROW EXECUTE FUNCTION pulse_test_tracking_gate()`); e != nil {
		t.Fatal(e)
	}
	trackDone := make(chan error, 1)
	go func() {
		_, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "downgrade-race-admit", "downgrade-race-result")
		trackDone <- e
	}()
	drainedTrack := false
	defer func() {
		if !drainedTrack {
			cancel()
			release, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_, _ = holder.Exec(release, `SELECT pg_advisory_unlock($1)`, key)
			<-trackDone
		}
	}()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	var writer uint32
	if e = f.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, holder.Conn().PgConn().PID()).Scan(&writer); e != nil {
		t.Fatal(e)
	}
	downDone := make(chan error, 1)
	go func() {
		_, e := postgres.Rollback(ctx, f.dsn, filepath.Join("..", "..", "..", "migrations"))
		downDone <- e
	}()
	drainedDown := false
	defer func() {
		if !drainedDown {
			cancel()
			<-downDone
		}
	}()
	waitPreparationBlock(t, f.p, writer)
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key); e != nil {
		t.Fatal(e)
	}
	e = <-downDone
	drainedDown = true
	if e == nil || !strings.Contains(e.Error(), "upgrade tracking history prevents downgrade") {
		t.Fatal("concurrent downgrade lost permit history", e)
	}
	e = <-trackDone
	drainedTrack = true
	if e != nil {
		t.Fatal("admission/result did not survive rejected downgrade", e)
	}
	trackingCounts(t, f, 1, 1, 1)
	if e = postgres.CheckSchemaVersion(ctx, f.p, 72); e != nil {
		t.Fatal(e)
	}
}

func verifyTrackingOrphanRestore(t *testing.T, f *trackingFixture) {
	t.Helper()
	ctx := context.Background()
	dump := filepath.Join(t.TempDir(), "orphan-tracking72.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	p, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	repo := trackingRepositoryForPool(t, f, p)
	calls := f.infos.Load()
	for _, key := range []string{"orphan-admit", "restored-fresh-admit"} {
		if _, e = repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), key, "orphan-result"); !errors.Is(e, ports.ErrMovieUpgradeTrackingReview) {
			t.Fatal("restored orphan permit was taken over", e)
		}
	}
	var permits, results int
	if e = p.QueryRow(ctx, `SELECT (SELECT count(*) FROM movie_upgrade_tracking_permit),(SELECT count(*) FROM movie_upgrade_tracking_result)`).Scan(&permits, &results); e != nil || permits != 1 || results != 0 || f.infos.Load() != calls {
		t.Fatal("restore changed orphan physical barrier", permits, results, e)
	}
	if _, e = postgres.Rollback(ctx, restored, filepath.Join("..", "..", "..", "migrations")); e == nil || !strings.Contains(e.Error(), "upgrade tracking history prevents downgrade") {
		t.Fatal("restored orphan permit downgraded", e)
	}
}
