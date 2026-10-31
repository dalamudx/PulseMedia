package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type frozenNoCalls struct{ calls int }

func (p *frozenNoCalls) AddTorrent(context.Context, ports.AddTorrentParam) error {
	p.calls++
	return errors.New("unexpected Add")
}
func (p *frozenNoCalls) GetTorrent(context.Context, string) (*ports.TorrentInfo, error) {
	p.calls++
	return nil, errors.New("unexpected observation")
}
func (p *frozenNoCalls) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	p.calls++
	return nil, errors.New("unexpected list")
}
func (p *frozenNoCalls) RemoveTorrent(context.Context, string, bool) error {
	p.calls++
	return errors.New("unexpected remove")
}
func (p *frozenNoCalls) Test(context.Context) error { p.calls++; return errors.New("unexpected test") }

// Expected failure on unmodified manager.go: Submit returns a cached completed
// task, Reconcile returns cached completed/tracking, Observe returns cached
// completed/unknown (or calls the client for tracking). All must reject first.
func TestFrozenUpgradeGenericEntryGuardsPG(t *testing.T) {
	for _, state := range []string{"none", "completed", "unknown", "tracking"} {
		t.Run(state, func(t *testing.T) {
			f := frozenUpgrade(t)
			f.enable(t)
			c := f.command(t)
			ctx := context.Background()
			r, e := f.store.ReserveUpgrade(ctx, f.actor, c, "guard")
			if e != nil {
				t.Fatal(e)
			}
			before := f.oldState(t)
			if state != "none" {
				// Seed historical/corrupt evidence solely in this owned test DB. Production
				// migration prevents new physical tasks for these operations.
				f.exec(t, "ALTER TABLE download_task DISABLE TRIGGER upgrade_no_physical_task")
				if _, e = db.New(f.p).CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: uuid.New(), OperationID: r.OperationID, ClientID: f.client, CandidateID: f.candidate, InfoHash: "frozen-upgrade-probe", State: state, SavePath: "/never-open"}); e != nil {
					t.Fatal(e)
				}
				f.exec(t, "ALTER TABLE download_task ENABLE TRIGGER upgrade_no_physical_task")
			}
			m := download.NewManager(f.p, idgen.New(), make([]byte, 32))
			probe := &frozenNoCalls{}
			m.RegisterClient(f.client, probe)
			if _, e = m.PrepareDownload(ctx, r.OperationID, f.client, f.candidate, "/never-open"); !errors.Is(e, ports.ErrUpgradeNotExecutable) {
				t.Errorf("Submit bypassed upgrade guard: %v", e)
			}
			if _, e = m.ReconcileTask(ctx, r.OperationID); !errors.Is(e, ports.ErrUpgradeNotExecutable) {
				t.Errorf("Reconcile bypassed upgrade guard: %v", e)
			}
			if _, e = m.ObserveTask(ctx, r.OperationID); !errors.Is(e, ports.ErrUpgradeNotExecutable) {
				t.Errorf("Observe bypassed upgrade guard: %v", e)
			}
			if probe.calls != 0 {
				t.Errorf("physical client calls=%d", probe.calls)
			}
			if before != f.oldState(t) {
				t.Fatal("generic entry changed old file/fact")
			}
		})
	}
}

func TestFrozenUpgradeExpiryAfterCandidateWaitPG(t *testing.T) {
	f := frozenUpgrade(t)
	f.enable(t)
	f.exec(t, "UPDATE unit_fact SET expires_at=clock_timestamp()+interval '600 milliseconds' WHERE id=$1", f.fact)
	f.project(t)
	c := f.command(t)
	ctx := context.Background()
	tx, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, tx)
	if _, e = tx.Exec(ctx, "SELECT id FROM release_candidate WHERE id=$1 FOR UPDATE", f.candidate); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.store.ReserveUpgrade(ctx, f.actor, c, "expired-after-wait"); done <- e }()
	select {
	case e := <-done:
		t.Fatalf("did not wait for candidate %v", e)
	case <-time.After(700 * time.Millisecond):
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ports.ErrUpgradeReservationConflict) {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stuck")
	}
	f.assertNoReservation(t)
}

func frozenAcquireOtherRoot(t *testing.T, f *frozenUpgradeFixture) (*postgres.AcquisitionStore, ports.AcquireMovieCommand) {
	t.Helper()
	ctx := context.Background()
	q := db.New(f.p)
	// Independent acquire policy rejects the existing old filename, so a normal
	// acquire in another root reaches global lane arbitration rather than local
	// reuse. The upgrade remains bound to its original immutable policy.
	rules, _ := json.Marshal(quality.PolicyRules{CutoffScore: 1000, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res2160p: 400}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}, RejectTerms: []string{"original"}})
	if _, e := q.InsertPolicyRevision(ctx, db.InsertPolicyRevisionParams{ID: uuid.New(), PolicyID: "default", Revision: 2, Rules: rules}); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE policy SET current_revision=2 WHERE id='default'")
	root := uuid.New()
	if _, e := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: "/never-open-acquire-other", Name: "Other", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1", root)
	return postgres.NewAcquisitionStore(f.p, idgen.New(), clock.New(), acquisition.SelectMovieRound), ports.AcquireMovieCommand{UnitIDs: []uuid.UUID{f.unit}, RootID: root, CandidateID: f.candidate, DownloaderID: &f.client, IdempotencyKey: "ordinary-other-root"}
}
func TestFrozenUpgradeAcquireLaneBothOrdersPG(t *testing.T) {
	t.Run("acquire_first", func(t *testing.T) {
		f := frozenUpgrade(t)
		f.enable(t)
		a, c := frozenAcquireOtherRoot(t, f)
		acquired, e := a.AcquireMovie(context.Background(), f.actor, c)
		if e != nil {
			t.Fatal(e)
		}
		f.project(t)
		upgrade := f.command(t)
		if _, e = f.store.ReserveUpgrade(context.Background(), f.actor, upgrade, "after-acquire"); !errors.Is(e, ports.ErrAcquisitionUnitAlreadyReserved) {
			t.Fatal("upgrade reused acquire lane", e)
		}
		var authority, lanes int
		if e = f.p.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM movie_upgrade_authority),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1)", acquired.OperationID).Scan(&authority, &lanes); e != nil || authority != 0 || lanes != 1 {
			t.Fatal(authority, lanes, e)
		}
	})
	t.Run("upgrade_first", func(t *testing.T) {
		f := frozenUpgrade(t)
		f.enable(t)
		a, c := frozenAcquireOtherRoot(t, f)
		upgrade := f.command(t)
		r, e := f.store.ReserveUpgrade(context.Background(), f.actor, upgrade, "before-acquire")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = a.AcquireMovie(context.Background(), f.actor, c); !errors.Is(e, ports.ErrAcquisitionUnitAlreadyReserved) {
			t.Fatal("acquire reused upgrade lane", e)
		}
		var requirements, lanes, jobs int
		if e = f.p.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM download_requirement WHERE operation_id=$1),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1),(SELECT count(*) FROM job)", r.OperationID).Scan(&requirements, &lanes, &jobs); e != nil || requirements != 1 || lanes != 1 || jobs != 0 {
			t.Fatal(requirements, lanes, jobs, e)
		}
	})
}

func TestFrozenUpgradeDDLAndDMLRolesPG(t *testing.T) {
	ctx := context.Background()
	cluster := pgtest.GetCluster(t)
	dsn := cluster.NewTestDB(t)
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	ddl, dml := cluster.CreateRoles(t, cfg.Database)
	if v, e := postgres.Migrate(ctx, ddl, frozenMigrationDir(t)); e != nil || v != 64 {
		t.Fatal(v, e)
	}
	pool, e := pgxpool.New(ctx, dml)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if _, e = pool.Exec(ctx, "CREATE TABLE forbidden_upgrade_ddl(id int)"); e == nil {
		t.Fatal("DML role performed DDL")
	}
	if _, e = pool.Exec(ctx, "SELECT * FROM movie_upgrade_authority"); e != nil {
		t.Fatal("new authority not readable by DML", e)
	}
	if _, e = pool.Exec(ctx, "UPDATE subscription SET upgrade_mode='disabled' WHERE false"); e != nil {
		t.Fatal("mode not writable by DML", e)
	}
}

func frozenPGCommand(t *testing.T, ctx context.Context, bin, dsn string, args ...string) {
	t.Helper()
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "PGHOST="+cfg.Host, "PGPORT="+strconv.Itoa(int(cfg.Port)), "PGUSER="+cfg.User, "PGPASSWORD="+cfg.Password, "PGDATABASE="+cfg.Database)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("%s failed: %s (%v)", bin, out, e)
	}
}
func TestFrozenUpgradeBackupRestorePG(t *testing.T) {
	f := frozenUpgrade(t)
	f.enable(t)
	c := f.command(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, e := f.store.ReserveUpgrade(ctx, f.actor, c, "restore")
	if e != nil {
		t.Fatal(e)
	}
	dump := filepath.Join(t.TempDir(), "upgrade.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restore := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restore, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restore), dump)
	p, e := pgxpool.New(ctx, restore)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = postgres.CheckSchemaVersion(ctx, p, 64); e != nil {
		t.Fatal(e)
	}
	got, e := postgres.NewUpgradeReservationStore(p, idgen.New()).ReserveUpgrade(ctx, f.actor, c, "restore")
	if e != nil || *got != *r {
		t.Fatal("restored exact receipt", got, e)
	}
	for _, table := range []string{"movie_upgrade_authority", "acquisition_reservation", "unit_reservation", "download_requirement", "library_file", "unit_fact"} {
		var original, copy string
		query := fmt.Sprintf("SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]'::jsonb)::text FROM %s x", table)
		if e = f.p.QueryRow(ctx, query).Scan(&original); e != nil {
			t.Fatal(e)
		}
		if e = p.QueryRow(ctx, query).Scan(&copy); e != nil {
			t.Fatal(e)
		}
		if original != copy {
			t.Fatal("restore drift", table)
		}
	}
	if _, e = p.Exec(ctx, "DELETE FROM movie_upgrade_authority WHERE operation_id=$1", r.OperationID); e == nil {
		t.Fatal("restore lost immutable guard")
	}
}
func mustFrozenDB(t *testing.T, dsn string) string {
	t.Helper()
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	return cfg.Database
}
