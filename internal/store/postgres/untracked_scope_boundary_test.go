package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type untrackedExpiryClock struct{ expired atomic.Bool }

func (c *untrackedExpiryClock) Now() time.Time { return time.Now() }
func (c *untrackedExpiryClock) Since(at time.Time) time.Duration {
	if c.expired.Load() {
		return 15 * time.Second
	}
	return time.Since(at)
}

func TestUntrackedScopeDeadlineAndInstancePG(t *testing.T) {
	f := newUntrackedFixture(t)
	clock := &untrackedExpiryClock{}
	f.store = postgres.NewUntrackedScopeStore(f.p, idgen.New(), clock)
	c := f.prepare(t, f.in, "deadline")
	evidence := ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 2, Inode: 222}, TargetIdentity: ports.FileIdentity{Device: 1, Inode: 111}}
	other := postgres.NewUntrackedScopeStore(f.p, idgen.New(), clock)
	_, err := other.CommitUntrackedScope(t.Context(), c, evidence)
	checkinErr(t, err, ports.ErrRejected)
	clock.expired.Store(true)
	_, err = f.store.CommitUntrackedScope(t.Context(), c, evidence)
	checkinErr(t, err, ports.ErrEndpointVersionConflict)
}

func TestUntrackedScopeSameRootAndEarliestBaselinePG(t *testing.T) {
	f := newUntrackedFixture(t)
	f.exec(t, `UPDATE download_client SET remote_path_map='{"/remote":"/scope-target"}' WHERE id=$1`, f.client)
	later := uuid.New()
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, later, f.root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,9,999,true,clock_timestamp() FROM library_root WHERE id=$2`, later, f.root)
	c := f.prepare(t, f.in, "same-root")
	m := c.UntrackedScopeMaterial()
	if m.TargetBaselineScanID == later || m.TargetIdentity.Inode != 111 || m.LocalRoot != "/scope-target" {
		t.Fatal(m)
	}
	r, err := f.store.CommitUntrackedScope(t.Context(), c, ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 1, Inode: 111}, TargetIdentity: ports.FileIdentity{Device: 1, Inode: 111}})
	if err != nil || r.Scope.SourceIdentity != r.Scope.TargetIdentity {
		t.Fatal(r, err)
	}
	f.guard(t, f.root, "movie.mkv", ports.ErrMovieLinkStale)
}

func TestUntrackedScopeDisabledCreateAndMissingRelationPG(t *testing.T) {
	f := newUntrackedFixture(t)
	in := f.in
	in.Enabled = false
	c := f.prepare(t, in, "disabled")
	r, err := f.store.CommitUntrackedScope(t.Context(), c, ports.UntrackedScopeEvidence{})
	if err != nil || r.Scope.Enabled || r.Scope.SourceIdentity != (ports.FileIdentity{}) {
		t.Fatal(r, err)
	}
	f.guard(t, f.root, "movie.mkv", nil)
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), `ALTER TABLE untracked_directory_scope_receipt RENAME TO untracked_scope_test_hidden`); err != nil {
		t.Fatal(err)
	}
	checkinErr(t, postgres.GuardUntrackedMovieRecognition(t.Context(), db.New(tx), f.root, "movie.mkv"), nil)
}

func TestUntrackedScopeEnableWaitsForSourceRootPG(t *testing.T) {
	f := newUntrackedFixture(t)
	source := uuid.New()
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type) VALUES($1,'/scope-source','source','movie')`, source)
	c := f.prepare(t, f.in, "enable")
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var id uuid.UUID
	if err = tx.QueryRow(t.Context(), `SELECT id FROM library_root WHERE id=$1 FOR UPDATE`, source).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := f.store.CommitUntrackedScope(ctx, c, ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 2, Inode: 222}, TargetIdentity: ports.FileIdentity{Device: 1, Inode: 111}})
		done <- e
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = f.p.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("enable did not wait for source root")
		}
		time.Sleep(5 * time.Millisecond)
	}
	checkinErr(t, postgres.GuardUntrackedMovieRecognition(t.Context(), db.New(tx), source, "movie.mkv"), nil)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	err = <-done
	joined = true
	if err != nil {
		t.Fatal(err)
	}
	f.guard(t, source, "movie.mkv", ports.ErrMovieLinkStale)
}

func TestUntrackedScopeDatabaseDeadlineTailPG(t *testing.T) {
	f := newUntrackedFixture(t)
	c := f.prepare(t, f.in, "deadline-tail")
	f.exec(t, `CREATE FUNCTION untracked_deadline_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(15.05); RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER untracked_deadline_tail AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION untracked_deadline_tail()`)
	_, err := f.store.CommitUntrackedScope(t.Context(), c, ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 2, Inode: 222}, TargetIdentity: ports.FileIdentity{Device: 1, Inode: 111}})
	checkinErr(t, err, ports.ErrEndpointVersionConflict)
	var n int
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM untracked_directory_scope_receipt`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestUntrackedScopeDowngradeHistoryPG(t *testing.T) {
	f := newUntrackedFixture(t, 102)
	f.save(t, f.in, "history")
	raw, err := os.ReadFile("../../../migrations/00102_untracked_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, ok := strings.Cut(string(raw), "-- +goose Down")
	if !ok {
		t.Fatal("missing down migration")
	}
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), down); err == nil {
		t.Fatal("downgrade erased history")
	}
}

func TestUntrackedScopeHistoricalSchemaPG(t *testing.T) {
	f := newUntrackedFixture(t)
	ctx := t.Context()
	version := int64(postgres.CurrentMigrationVersion)
	for version > 101 {
		var err error
		version, err = postgres.Rollback(ctx, f.dsn, "../../../migrations")
		if err != nil {
			t.Fatal("empty review scope downgrade", err)
		}
	}
	if version != 101 {
		t.Fatal("expected original pre-scope schema", version)
	}
	if err := postgres.GuardUntrackedMovieRecognition(ctx, db.New(f.p), f.root, "original.mkv"); err != nil {
		t.Fatal("legacy recognition was blocked without scope history", err)
	}
	if version, err := postgres.Migrate(ctx, f.dsn, "../../../migrations"); err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatal("restore current schema", version, err)
	}
}
