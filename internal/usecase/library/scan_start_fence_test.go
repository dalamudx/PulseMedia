package library_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type rootProbeHook struct {
	ports.TransferRoot
	hook func() error
}

func (r *rootProbeHook) Inspect(ctx context.Context, rel string) (ports.FileIdentity, error) {
	if err := r.hook(); err != nil {
		return ports.FileIdentity{}, err
	}
	return r.TransferRoot.Inspect(ctx, rel)
}

type startFenceFilesystem struct {
	ports.ScanFilesystem
	hook   func() error
	walked int
}

func (f *startFenceFilesystem) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, err := f.ScanFilesystem.LockRoots(ctx, paths...)
	if err != nil {
		return nil, nil, err
	}
	for path, root := range roots {
		roots[path] = &rootProbeHook{TransferRoot: root, hook: f.hook}
	}
	return roots, release, nil
}
func (f *startFenceFilesystem) Walk(ctx context.Context, root ports.TransferRoot, visit func(string, ports.FileIdentity) error) error {
	f.walked++
	if wrapped, ok := root.(*rootProbeHook); ok {
		root = wrapped.TransferRoot
	}
	return f.ScanFilesystem.Walk(ctx, root, visit)
}

func TestScanStartRejectsChangedProbedRoot(t *testing.T) {
	for _, field := range []string{"path", "media_type", "marker_filename", "generation", "health_revision", "missing_marker_path"} {
		t.Run(field, func(t *testing.T) {
			_, q, dsn := setupScannerTest(t)
			ctx := context.Background()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			path, replacement := t.TempDir(), t.TempDir()
			if field != "missing_marker_path" {
				if err = os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("fixture marker"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(filepath.Join(path, "Old.Movie.2024.mkv"), []byte("old root content"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Movies"})
			if err != nil {
				t.Fatal(err)
			}
			var frozen string
			fs := &startFenceFilesystem{ScanFilesystem: rootfs.NewFilesystem()}
			fs.hook = func() error {
				var e error
				switch field {
				case "path", "missing_marker_path":
					_, e = pool.Exec(ctx, "UPDATE library_root SET path=$2 WHERE id=$1", root.ID, replacement)
				case "media_type":
					_, e = pool.Exec(ctx, "UPDATE library_root SET media_type='tv' WHERE id=$1", root.ID)
				case "marker_filename":
					_, e = pool.Exec(ctx, "UPDATE library_root SET marker_filename='.new_marker' WHERE id=$1", root.ID)
				case "generation":
					_, e = pool.Exec(ctx, "UPDATE library_root SET generation=generation+1 WHERE id=$1", root.ID)
				case "health_revision":
					_, e = pool.Exec(ctx, "UPDATE library_root SET health_revision=health_revision+1 WHERE id=$1", root.ID)
				}
				if e != nil {
					return e
				}
				return pool.QueryRow(ctx, "SELECT to_jsonb(r)::text FROM library_root r WHERE id=$1", root.ID).Scan(&frozen)
			}
			scanner := library.NewScanner(idgen.New(), fs, postgres.NewScanObservationStore(pool))
			_, err = scanner.ScanRoot(ctx, root.ID)
			if err == nil {
				t.Fatal("changed root allowed stale capability to start or finish scan")
			}
			if fs.walked != 0 {
				t.Fatalf("stale root walked %d times", fs.walked)
			}
			var after string
			var writes int64
			if err = pool.QueryRow(ctx, "SELECT to_jsonb(r)::text FROM library_root r WHERE id=$1", root.ID).Scan(&after); err != nil || frozen != after {
				t.Fatalf("stale probe modified replacement root: %v", err)
			}
			if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM scan_run WHERE root_id=$1)+(SELECT count(*) FROM library_file WHERE root_id=$1)+(SELECT count(*) FROM scan_observation WHERE root_id=$1)", root.ID).Scan(&writes); err != nil || writes != 0 {
				t.Fatalf("stale probe persisted %d writes: %v", writes, err)
			}
		})
	}
}

func TestScanStartAndProbeWriteFailureLeavesNoPartialState(t *testing.T) {
	for _, kind := range []string{"health_offline", "health_online", "start"} {
		t.Run(kind, func(t *testing.T) {
			_, q, dsn := setupScannerTest(t)
			ctx := context.Background()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			path := t.TempDir()
			if kind != "health_offline" {
				if err = os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("fixture marker"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Movies"})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "health_online" {
				if _, err = q.UpdateRootHealth(ctx, db.UpdateRootHealthParams{ID: root.ID, Health: "offline"}); err != nil {
					t.Fatal(err)
				}
			}
			var before, after string
			if err = pool.QueryRow(ctx, "SELECT to_jsonb(r)::text FROM library_root r WHERE id=$1", root.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_scan_start_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic scan start write failure'; END $$`); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER reject_scan_start_write BEFORE UPDATE OF health ON library_root FOR EACH ROW EXECUTE FUNCTION reject_scan_start_write()`
			if kind == "start" {
				trigger = `CREATE TRIGGER reject_scan_start_write BEFORE INSERT ON scan_run FOR EACH ROW EXECUTE FUNCTION reject_scan_start_write()`
			}
			if _, err = pool.Exec(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			fs := &startFenceFilesystem{ScanFilesystem: rootfs.NewFilesystem(), hook: func() error { return nil }}
			scanner := library.NewScanner(idgen.New(), fs, postgres.NewScanObservationStore(pool))
			if _, err = scanner.ScanRoot(ctx, root.ID); err == nil || fs.walked != 0 {
				t.Fatalf("failed start continued scan: walked=%d err=%v", fs.walked, err)
			}
			if err = pool.QueryRow(ctx, "SELECT to_jsonb(r)::text FROM library_root r WHERE id=$1", root.ID).Scan(&after); err != nil || before != after {
				t.Fatalf("failed start left root mutation: %v", err)
			}
			var count int64
			if err = pool.QueryRow(ctx, "SELECT count(*) FROM scan_run WHERE root_id=$1", root.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed start persisted run: %v", err)
			}
			waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, release, err := rootfs.NewFilesystem().LockRoots(waitCtx, path)
			cancel()
			if err != nil {
				t.Fatalf("failed start leaked physical root slot: %v", err)
			}
			release()
			if kind == "start" {
				_, err = pool.Exec(ctx, "DROP TRIGGER reject_scan_start_write ON scan_run")
			} else {
				_, err = pool.Exec(ctx, "DROP TRIGGER reject_scan_start_write ON library_root")
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "health_offline" {
				if err = os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("restored fixture marker"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := scanner.ScanRoot(ctx, root.ID)
			if err != nil || report.State != "succeeded" {
				t.Fatalf("valid scan after fault removal failed: %v", err)
			}
		})
	}
}
