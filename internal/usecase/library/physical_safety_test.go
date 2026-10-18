package library_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanWaitsForPhysicalTransferSlot(t *testing.T) {
	scanner, q, _ := setupScannerTest(t)
	ctx := context.Background()
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("fixture root"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := rootfs.NewFilesystem().LockRoots(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	if _, err := scanner.ScanRoot(waitCtx, root.ID); !errors.Is(err, context.DeadlineExceeded) {
		cancel()
		t.Fatalf("scan bypassed physical writer: %v", err)
	}
	cancel()
	row, err := q.GetLibraryRootByID(ctx, root.ID)
	if err != nil || row.BaselineCompleted {
		t.Fatal("blocked scan unlocked baseline")
	}
	release()
	release = nil
	if _, err := scanner.ScanRoot(ctx, root.ID); err != nil {
		t.Fatalf("scan after physical I/O completed: %v", err)
	}
}

func TestSymlinkScanDoesNotManufactureMissingOrBaseline(t *testing.T) {
	scanner, q, _ := setupScannerTest(t)
	ctx := context.Background()
	path, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("fixture root"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: "protected.mkv", SizeBytes: 3, MtimeNs: 1, LastMutationSeq: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ScanRoot(ctx, root.ID); err == nil {
		t.Fatal("symlink scan claimed a complete baseline")
	}
	row, err := q.GetLibraryRootByID(ctx, root.ID)
	if err != nil || row.BaselineCompleted {
		t.Fatal("partial scan unlocked baseline")
	}
	observed, err := q.GetFileByID(ctx, file.ID)
	if err != nil || observed.RetiredAt.Valid {
		t.Fatalf("partial scan retired existing evidence: %v %v", observed, err)
	}
}

func TestScanSuccessRejectsStaleTokenAndOfflineRoot(t *testing.T) {
	_, q, _ := setupScannerTest(t)
	ctx := context.Background()
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "movie", Name: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := q.StartScanRun(ctx, db.StartScanRunParams{ID: root.ID, ID_2: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	current, err := q.StartScanRun(ctx, db.StartScanRunParams{ID: root.ID, ID_2: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.FinishScanRunSuccess(ctx, db.FinishScanRunSuccessParams{ID: old.ID}); err == nil {
		t.Fatal("superseded scan claimed successful baseline")
	}
	if _, err := q.UpdateRootHealth(ctx, db.UpdateRootHealthParams{ID: root.ID, Health: "offline"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.FinishScanRunSuccess(ctx, db.FinishScanRunSuccessParams{ID: current.ID}); err == nil {
		t.Fatal("offline scan claimed successful baseline")
	}
	row, err := q.GetLibraryRootByID(ctx, root.ID)
	if err != nil || row.BaselineCompleted {
		t.Fatal("invalid scan unlocked baseline")
	}
}
