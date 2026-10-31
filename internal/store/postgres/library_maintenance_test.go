package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/usecase/librarymaintenance"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type maintenanceTestFS struct {
	ports.LibraryMaintenanceFilesystem
	calls  atomic.Int32
	before func(context.Context)
	after  func(context.Context)
}

func (f *maintenanceTestFS) MaintenanceRename(ctx context.Context, r ports.TransferRoot, a, b string, id ports.FileIdentity) error {
	f.calls.Add(1)
	if f.before != nil {
		f.before(ctx)
	}
	e := f.LibraryMaintenanceFilesystem.MaintenanceRename(ctx, r, a, b, id)
	if f.after != nil {
		f.after(ctx)
	}
	return e
}
func (f *maintenanceTestFS) MaintenanceRemove(ctx context.Context, r ports.TransferRoot, a string, id ports.FileIdentity) error {
	f.calls.Add(1)
	if f.before != nil {
		f.before(ctx)
	}
	e := f.LibraryMaintenanceFilesystem.MaintenanceRemove(ctx, r, a, id)
	if f.after != nil {
		f.after(ctx)
	}
	return e
}

type maintenanceFixture struct {
	pool             *pgxpool.Pool
	store            *LibraryMaintenanceStore
	files            *maintenanceTestFS
	actor            *ports.Actor
	file, root, unit uuid.UUID
	dir              string
}

func newMaintenanceFixture(t *testing.T) *maintenanceFixture {
	t.Helper()
	ctx := t.Context()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := Migrate(ctx, dsn, "../../../migrations"); e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	f := &maintenanceFixture{pool: pool, file: uuid.New(), root: uuid.New(), unit: uuid.New(), dir: t.TempDir(), actor: &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}}
	f.files = &maintenanceTestFS{LibraryMaintenanceFilesystem: rootfs.NewFilesystem()}
	f.store, e = NewLibraryMaintenanceStore(pool, f.files, idgen.New())
	if e != nil {
		t.Fatal(e)
	}
	for name, content := range map[string]string{".pulsemedia_root": "owned-root", "original.mkv": "unchanged media bytes"} {
		if e = os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	root, e := rootfs.Open(f.dir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	ri, e := root.Identity()
	if e != nil {
		t.Fatal(e)
	}
	fi, e := root.Inspect("original.mkv")
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'maintenance-admin','unused','admin')`, f.actor.ID)
	media := uuid.New()
	f.exec(t, `INSERT INTO media(id,title,media_type) VALUES($1,'Maintenance Movie','movie')`, media)
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, f.unit, media)
	f.exec(t, `INSERT INTO library_root(id,path,media_type,name,health,baseline_completed) VALUES($1,$2,'movie','Maintenance','online',true)`, f.root, f.dir)
	f.exec(t, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,'original.mkv',$3,$4,$5)`, f.file, f.root, fi.Size, fi.Mtime, fmt.Sprintf("stat.v1:%d:%d:%d", fi.Inode, fi.Size, fi.Mtime))
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, f.file, f.unit)
	profile, _ := json.Marshal(quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion})
	f.exec(t, `INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) VALUES($1,$2,'imported',$3,$4,$5)`, uuid.New(), f.unit, f.file, f.root, profile)
	scan := uuid.New()
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state) VALUES($1,$2,0,1,0,'succeeded')`, scan, f.root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,$3,$4,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root, ri.Device, ri.Inode)
	return f
}
func (f *maintenanceFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, e := f.pool.Exec(t.Context(), q, args...); e != nil {
		t.Fatal(e)
	}
}
func (f *maintenanceFixture) preview(t *testing.T) ports.LibraryMaintenanceCommand {
	t.Helper()
	p, e := f.store.PreviewReorganization(t.Context(), f.actor, ports.LibraryReorganizationRequest{FileID: f.file, TargetRelativePath: "Renamed/movie.mkv"})
	if e != nil {
		t.Fatal("preview", e)
	}
	if !p.CanConfirm || p.ExecutionAllowed {
		t.Fatal(p)
	}
	return ports.LibraryMaintenanceCommand{Action: "reorganize", FileID: f.file, TargetRelativePath: p.TargetRelativePath, ExpectedVersion: p.Version, Confirmed: true}
}
func TestLibraryMaintenanceReorganizationPG(t *testing.T) {
	f := newMaintenanceFixture(t)
	before, e := os.Stat(filepath.Join(f.dir, "original.mkv"))
	if e != nil {
		t.Fatal(e)
	}
	c := f.preview(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first *ports.LibraryMaintenanceResult
	for range 4 {
		wg.Go(func() {
			out, e := librarymaintenance.NewService(f.store).Command(t.Context(), f.actor, c, "rename-once")
			mu.Lock()
			defer mu.Unlock()
			if e != nil || out == nil {
				t.Errorf("command: %+v %v", out, e)
				return
			}
			if first == nil {
				first = out
			} else if *first != *out {
				t.Errorf("unstable receipt: %+v %+v", first, out)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if first.Status != "completed" || f.files.calls.Load() != 1 {
		t.Fatalf("not completed once: %+v calls=%d", first, f.files.calls.Load())
	}
	after, e := os.Stat(filepath.Join(f.dir, c.TargetRelativePath))
	if e != nil || !os.SameFile(before, after) {
		t.Fatal("identity lost", e)
	}
	if _, e = os.Stat(filepath.Join(f.dir, "original.mkv")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("source remains", e)
	}
	var valid bool
	e = f.pool.QueryRow(t.Context(), `SELECT f.rel_path=$2 AND f.retired_at IS NULL AND (SELECT count(*) FROM library_file_unit WHERE file_id=f.id AND retired_at IS NULL)=1 AND (SELECT count(*) FROM unit_fact WHERE source_file_id=f.id AND revoked_at IS NULL)=1 AND (SELECT count(*) FROM audit_log WHERE action='library.maintenance.reorganize')=1 AND (SELECT count(*) FROM outbox_event WHERE event_type='library.maintenance.reorganize')=1 FROM library_file f WHERE id=$1`, f.file, c.TargetRelativePath).Scan(&valid)
	if e != nil || !valid {
		t.Fatal("atomic file, facts and ledger", valid, e)
	}
	recovered, e := f.store.ReadCommand(t.Context(), f.actor, "rename-once")
	if e != nil || *recovered != *first {
		t.Fatal(recovered, e)
	}
	c.TargetRelativePath = "wrong.mkv"
	if _, e = librarymaintenance.NewService(f.store).Command(t.Context(), f.actor, c, "rename-once"); e == nil {
		t.Fatal("rebound key")
	}
	for _, q := range []string{`UPDATE library_maintenance_command SET action='cleanup'`, `DELETE FROM library_maintenance_permit`, `TRUNCATE library_maintenance_return`} {
		if _, e = f.pool.Exec(t.Context(), q); e == nil {
			t.Fatal("mutable history", q)
		}
	}
}
func TestLibraryMaintenanceReorganizationRefusalPG(t *testing.T) {
	for _, kind := range []string{"target-collision", "symlink-parent", "changed-file", "permission", "mapping", "root-generation", "cancel-before", "baseline-replaced"} {
		t.Run(kind, func(t *testing.T) {
			f := newMaintenanceFixture(t)
			c := f.preview(t)
			ctx := t.Context()
			switch kind {
			case "target-collision":
				if e := os.Mkdir(filepath.Join(f.dir, "Renamed"), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(f.dir, c.TargetRelativePath), []byte("protected target"), 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink-parent":
				outside := t.TempDir()
				if e := os.Symlink(outside, filepath.Join(f.dir, "Renamed")); e != nil {
					t.Fatal(e)
				}
			case "changed-file":
				if e := os.WriteFile(filepath.Join(f.dir, "original.mkv"), []byte("changed identity"), 0600); e != nil {
					t.Fatal(e)
				}
			case "permission":
				f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, f.actor.ID)
			case "mapping":
				unit := uuid.New()
				f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) SELECT $1,media_id,'second' FROM media_unit WHERE id=$2`, unit, f.unit)
				f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, f.file, unit)
			case "root-generation":
				f.exec(t, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.root)
			case "cancel-before":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "baseline-replaced":
				f.exec(t, `UPDATE scan_finalization SET root_inode=root_inode+1 WHERE root_id=$1`, f.root)
			}
			if _, e := librarymaintenance.NewService(f.store).Command(ctx, f.actor, c, "refuse"); e == nil {
				t.Fatal("accepted invalid input")
			}
			if f.files.calls.Load() != 0 {
				t.Fatal("called filesystem")
			}
			if _, e := os.Stat(filepath.Join(f.dir, "original.mkv")); e != nil {
				t.Fatal("lost source", e)
			}
			var n int
			if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM library_maintenance_permit`).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
		})
	}
}
func TestLibraryMaintenanceReturnedUnknownPG(t *testing.T) {
	for _, kind := range []string{"cancel-after", "rollback-audit", "revoke-during", "connection-loss", "root-tail-tamper", "commit-expiry"} {
		t.Run(kind, func(t *testing.T) {
			f := newMaintenanceFixture(t)
			c := f.preview(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch kind {
			case "cancel-after":
				f.files.after = func(context.Context) { cancel() }
			case "rollback-audit":
				f.exec(t, `CREATE FUNCTION maintenance_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='library.maintenance.reorganize' AND NEW.payload->>'status'='completed' THEN RAISE EXCEPTION 'rollback'; END IF; RETURN NEW; END $$`)
				f.exec(t, `CREATE TRIGGER maintenance_reject_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION maintenance_reject_audit()`)
			case "root-tail-tamper":
				f.exec(t, `CREATE FUNCTION maintenance_tail_tamper() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='library.maintenance.reorganize' AND NEW.payload->>'status'='completed' THEN UPDATE library_root SET mutation_seq=mutation_seq+10; END IF; RETURN NEW; END $$`)
				f.exec(t, `CREATE TRIGGER maintenance_tail_tamper BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION maintenance_tail_tamper()`)
			case "commit-expiry":
				f.exec(t, `CREATE FUNCTION maintenance_short_permit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.lease_until:=NEW.started_at+interval '400 milliseconds'; RETURN NEW; END $$`)
				f.exec(t, `CREATE TRIGGER aaa_maintenance_short_permit BEFORE INSERT ON library_maintenance_permit FOR EACH ROW EXECUTE FUNCTION maintenance_short_permit()`)
				f.exec(t, `CREATE FUNCTION maintenance_delay_return() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='completed' THEN PERFORM pg_sleep(0.7); END IF; RETURN NEW; END $$`)
				f.exec(t, `CREATE TRIGGER aaa_maintenance_delay_return BEFORE INSERT ON library_maintenance_return FOR EACH ROW EXECUTE FUNCTION maintenance_delay_return()`)
			case "revoke-during":
				f.files.after = func(context.Context) { f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, f.actor.ID) }
			case "connection-loss":
				f.files.after = func(context.Context) {
					f.exec(t, `SELECT pg_terminate_backend(backend_pid) FROM library_maintenance_permit`)
				}
			}
			out, e := librarymaintenance.NewService(f.store).Command(ctx, f.actor, c, "uncertain")
			if kind != "connection-loss" && (e != nil || out == nil || out.Status != "unknown_requires_review") {
				t.Fatalf("unknown missing: %+v %v", out, e)
			}
			if f.files.calls.Load() != 1 {
				t.Fatal("call count", f.files.calls.Load())
			}
			if _, e := os.Stat(filepath.Join(f.dir, c.TargetRelativePath)); e != nil {
				t.Fatal("physical result absent", e)
			}
			if kind == "revoke-during" {
				f.exec(t, `UPDATE app_user SET role='admin' WHERE id=$1`, f.actor.ID)
			}
			receipt, e := f.store.ReadCommand(t.Context(), f.actor, "uncertain")
			if e != nil || receipt == nil || receipt.Status != "unknown_requires_review" {
				t.Fatal(receipt, e)
			}
			if _, e = librarymaintenance.NewService(f.store).Command(t.Context(), f.actor, c, "uncertain"); e != nil {
				t.Fatal(e)
			}
			if f.files.calls.Load() != 1 {
				t.Fatal("replayed unknown")
			}
			var rel string
			if e = f.pool.QueryRow(t.Context(), `SELECT rel_path FROM library_file WHERE id=$1`, f.file).Scan(&rel); e != nil || rel != "original.mkv" {
				t.Fatal("unknown changed library", rel, e)
			}
		})
	}
}
func TestLibraryMaintenancePhysicalLockPG(t *testing.T) {
	f := newMaintenanceFixture(t)
	c := f.preview(t)
	entered, finish := make(chan struct{}), make(chan struct{})
	f.files.before = func(context.Context) { close(entered); <-finish }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = librarymaintenance.NewService(f.store).Command(ctx, f.actor, c, "blocked")
	}()
	<-entered
	cancel()
	rootsDone := make(chan struct{})
	go func() {
		defer close(rootsDone)
		_, release, e := f.files.LockRoots(t.Context(), f.dir)
		if e == nil {
			release()
		}
	}()
	select {
	case <-rootsDone:
		close(finish)
		<-done
		t.Fatal("physical lock released while call alive")
	case <-time.After(80 * time.Millisecond):
	}
	close(finish)
	<-done
	<-rootsDone
}
func TestLibraryMaintenanceValidation(t *testing.T) {
	for _, p := range []string{"../a.mkv", "/a.mkv", "a/../b.mkv", ".pulsemedia_recycle/a.mkv", "a\\b.mkv", "a/./b.mkv", "a.srt"} {
		if maintenancePath(p) {
			t.Fatal(p)
		}
	}
	raw := mustMaintenanceJSON(ports.LibraryMaintenanceCommand{Action: "restore"})
	var c ports.LibraryMaintenanceCommand
	if json.Unmarshal(raw, &c) != nil || maintenanceCommandValid(c) {
		t.Fatal("incomplete command valid")
	}
}
