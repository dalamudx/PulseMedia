package library_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var errFinalizationAck = errors.New("synthetic finalization acknowledgement lost")

type lostFinalizationAck struct{ ports.ScanRepository }

func (s lostFinalizationAck) ReconcileBatch(ctx context.Context, in ports.ScanSeal) (ports.ScanFinalizationProgress, error) {
	p, err := s.ScanRepository.ReconcileBatch(ctx, in)
	if err == nil && !p.Done {
		return p, errFinalizationAck
	}
	return p, err
}

type forbidFinalizationWalk struct{ ports.ScanFilesystem }

func (f forbidFinalizationWalk) Walk(context.Context, ports.TransferRoot, func(string, ports.FileIdentity) error) error {
	return errors.New("sealed traversal was walked again")
}

func TestScannerFinalizationProcessResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if dsn := os.Getenv("PULSE_SCAN_FINALIZE_CHILD_DSN"); dsn != "" {
		pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		root, err := uuid.Parse(os.Getenv("PULSE_SCAN_FINALIZE_CHILD_ROOT"))
		if err != nil {
			t.Fatal(err)
		}
		run, err := uuid.Parse(os.Getenv("PULSE_SCAN_FINALIZE_CHILD_RUN"))
		if err != nil {
			t.Fatal(err)
		}
		scanner := library.NewScanner(idgen.New(), forbidFinalizationWalk{rootfs.NewFilesystem()}, postgres.NewScanObservationStore(pool))
		report, err := scanner.ScanRoot(ctx, root)
		if err != nil || report.ScanRunID != run || report.FilesObserved != 1 || report.FilesAdded != 1 || report.MissingCandidates != 130 || report.State != "succeeded" {
			t.Fatalf("process resume failed: %+v %v", report, err)
		}
		return
	}
	_, q, dsn := setupScannerTest(t)
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("synthetic marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "observed.mkv"), []byte("synthetic video"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Finalizing"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 130; i++ {
		if _, err = q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: fmt.Sprintf("unseen-%d.mkv", i), SizeBytes: 1, MtimeNs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := postgres.NewScanObservationStore(pool)
	scanner := library.NewScanner(idgen.New(), rootfs.NewFilesystem(), lostFinalizationAck{store})
	if _, err = scanner.ScanRoot(ctx, root.ID); !errors.Is(err, errFinalizationAck) {
		t.Fatal("missing lost-ack fault", err)
	}
	pending, err := store.FindFinalizing(ctx, root.ID)
	if err != nil || pending == nil {
		t.Fatal("no durable cursor", err)
	}
	current, err := q.GetLibraryRootByID(ctx, root.ID)
	if err != nil || current.BaselineCompleted {
		t.Fatal("incomplete baseline published", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestScannerFinalizationProcessResume$", "-test.v")
	cmd.Env = append(os.Environ(), "PULSE_SCAN_FINALIZE_CHILD_DSN="+dsn, "PULSE_SCAN_FINALIZE_CHILD_ROOT="+root.ID.String(), "PULSE_SCAN_FINALIZE_CHILD_RUN="+pending.RunID.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child recovery: %v\n%s", err, out)
	}
	current, err = q.GetLibraryRootByID(ctx, root.ID)
	if err != nil || !current.BaselineCompleted || current.ScanToken != pending.ScanToken {
		t.Fatal("resume reset token or omitted baseline", err)
	}
	var rows int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM scan_run WHERE root_id=$1", root.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("resume created another traversal", rows, err)
	}
}

func TestScannerFinalizationRejectsReplacedDirectory(t *testing.T) {
	_, q, dsn := setupScannerTest(t)
	ctx := context.Background()
	parent := t.TempDir()
	path := filepath.Join(parent, "root")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("synthetic marker"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Replaced root"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 130; i++ {
		if _, err = q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: fmt.Sprintf("unseen-%d.mkv", i), SizeBytes: 1, MtimeNs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := postgres.NewScanObservationStore(pool)
	if _, err = library.NewScanner(idgen.New(), rootfs.NewFilesystem(), lostFinalizationAck{store}).ScanRoot(ctx, root.ID); !errors.Is(err, errFinalizationAck) {
		t.Fatal(err)
	}
	pending, err := store.FindFinalizing(ctx, root.ID)
	if err != nil || pending == nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, filepath.Join(parent, "original")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("replacement marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = library.NewScanner(idgen.New(), forbidFinalizationWalk{rootfs.NewFilesystem()}, store).ScanRoot(ctx, root.ID); !errors.Is(err, library.ErrScanInterrupted) {
		t.Fatal("replacement was accepted", err)
	}
	current, err := q.GetLibraryRootByID(ctx, root.ID)
	if err != nil || current.BaselineCompleted || current.Health != "offline" {
		t.Fatal("replacement baseline remained available", err)
	}
	var state string
	var missing int64
	if err = pool.QueryRow(ctx, "SELECT state,files_missing_candidates FROM scan_run WHERE id=$1", pending.RunID).Scan(&state, &missing); err != nil || state != "partial" || missing != 128 {
		t.Fatal(state, missing, err)
	}
	var active int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM library_file WHERE root_id=$1 AND retired_at IS NULL", root.ID).Scan(&active); err != nil || active != 130 {
		t.Fatal("replaced volume invalidated files", active, err)
	}
}
