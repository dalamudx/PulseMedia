package library_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestScannerBindsCompletedPhysicalRootAcrossRuns(t *testing.T) {
	scanner, q, dsn := setupScannerTest(t)
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := postgres.NewScanObservationStore(pool)
	work := t.TempDir()
	rootPath := filepath.Join(work, "root")
	create := func(content string) {
		t.Helper()
		if err := os.Mkdir(rootPath, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootPath, ".pulsemedia_root"), []byte("same marker copied to replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootPath, "Movie.mkv"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	create("original independently preserved movie bytes")
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: rootPath, MediaType: "movie", Name: "Physical root binding"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		report, err := scanner.ScanRoot(ctx, root.ID)
		if err != nil || report.State != "succeeded" {
			t.Fatalf("same root scan %d: report=%+v err=%v", i, report, err)
		}
	}
	original, err := repo.LoadRoot(ctx, root.ID)
	if err != nil || original.RootIdentity == nil {
		t.Fatalf("missing completed physical binding: %+v %v", original, err)
	}
	// Keep one real started run to prove the seal entry point also revalidates.
	runID := uuid.New()
	run, err := repo.Start(ctx, original, runID)
	if err != nil {
		t.Fatal(err)
	}
	before := rootBindingSnapshot(t, pool, root.ID)
	oldPath := filepath.Join(work, "original-root")
	if err := os.Rename(rootPath, oldPath); err != nil {
		t.Fatal(err)
	}
	create("different replacement movie with the same name and marker")
	capability, err := rootfs.NewFilesystem().OpenRoot(ctx, rootPath)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := capability.Identity(ctx)
	capability.Close()
	if err != nil {
		t.Fatal(err)
	}
	if identity.Device == original.RootIdentity.Device && identity.Inode == original.RootIdentity.Inode {
		t.Fatal("fixture did not replace physical root")
	}
	if _, err = scanner.ScanRoot(ctx, root.ID); !errors.Is(err, library.ErrScanInterrupted) {
		t.Fatalf("replacement accepted: %v", err)
	}
	forged := original
	forged.RootIdentity = &identity
	if _, err = repo.Start(ctx, forged, uuid.New()); !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatalf("direct start accepted replacement: %v", err)
	}
	seal := ports.ScanSeal{RunID: runID, RootID: root.ID, Path: root.Path, MediaType: root.MediaType, MarkerFilename: root.MarkerFilename, Generation: run.Generation, HealthRevision: original.HealthRevision, ScanToken: run.ScanToken, StartMutationSeq: run.StartMutationSeq, RootIdentity: identity}
	if err = repo.Seal(ctx, seal); !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatalf("direct seal accepted replacement: %v", err)
	}
	if after := rootBindingSnapshot(t, pool, root.ID); after != before {
		t.Fatal("replacement changed root, runs, files, mappings, facts or missing candidates")
	}
	if err = os.Rename(rootPath, filepath.Join(work, "rejected-root")); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(oldPath, rootPath); err != nil {
		t.Fatal(err)
	}
	report, err := scanner.ScanRoot(ctx, root.ID)
	if err != nil || report.State != "succeeded" || report.FilesAdded != 0 || report.FilesUpdated != 0 || report.MissingCandidates != 0 {
		t.Fatalf("original root did not recover: %+v %v", report, err)
	}
	// An explicit generation transition leaves the previous generation's binding
	// as history; it must not silently carry that identity into the new epoch.
	if _, err = pool.Exec(ctx, "UPDATE library_root SET generation=generation+1,baseline_completed=false WHERE id=$1", root.ID); err != nil {
		t.Fatal(err)
	}
	next, err := repo.LoadRoot(ctx, root.ID)
	if err != nil || next.RootIdentity != nil {
		t.Fatalf("generation carried old binding: %+v %v", next, err)
	}
}

func rootBindingSnapshot(t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID) string {
	t.Helper()
	var snapshot string
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
	'root',(SELECT to_jsonb(r) FROM library_root r WHERE id=$1),
	'runs',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM scan_run s WHERE root_id=$1),
	'files',(SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM library_file f WHERE root_id=$1),
	'facts',(SELECT jsonb_agg(to_jsonb(u) ORDER BY u.id) FROM unit_fact u WHERE root_id=$1),
	'mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.file_id,m.unit_id) FROM library_file_unit m JOIN library_file f ON f.id=m.file_id WHERE f.root_id=$1),
	'missing',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.file_id,c.first_run_id) FROM missing_candidate c JOIN library_file f ON f.id=c.file_id WHERE f.root_id=$1)
	)::text`, rootID).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
