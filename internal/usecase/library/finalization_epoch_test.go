package library_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestScannerFinalizationEpochChangeAllowsFreshTraversal(t *testing.T) {
	_, q, dsn := setupScannerTest(t)
	ctx := context.Background()
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("synthetic marker"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Epoch change"})
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
	if _, err = q.UpdateRootHealth(ctx, db.UpdateRootHealthParams{ID: root.ID, Health: "offline"}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpdateRootHealth(ctx, db.UpdateRootHealthParams{ID: root.ID, Health: "online"}); err != nil {
		t.Fatal(err)
	}
	var before string
	if err = pool.QueryRow(ctx, "SELECT to_jsonb(r)::text FROM library_root r WHERE id=$1", root.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = library.NewScanner(idgen.New(), forbidFinalizationWalk{rootfs.NewFilesystem()}, store).ScanRoot(ctx, root.ID); !errors.Is(err, library.ErrScanInterrupted) {
		t.Fatal("stale seal resumed", err)
	}
	var after string
	if err = pool.QueryRow(ctx, "SELECT to_jsonb(r)::text FROM library_root r WHERE id=$1", root.ID).Scan(&after); err != nil || before != after {
		t.Fatal("abort altered replacement epoch", err)
	}
	var state string
	if err = pool.QueryRow(ctx, "SELECT state FROM scan_run WHERE id=$1", pending.RunID).Scan(&state); err != nil || state != "partial" {
		t.Fatal(state, err)
	}
	report, err := library.NewScanner(idgen.New(), rootfs.NewFilesystem(), store).ScanRoot(ctx, root.ID)
	if err != nil || report.ScanRunID == pending.RunID || report.MissingCandidates != 130 || report.State != "succeeded" {
		t.Fatal("fresh traversal was blocked", report, err)
	}
}
