package library_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScannerInterruptionWriteErrorIsReportedAndSlotRecovers(t *testing.T) {
	_, q, dsn := setupScannerTest(t)
	ctx := context.Background()
	path := t.TempDir()
	for name, data := range map[string]string{".pulsemedia_root": "synthetic marker", "A.Movie.mkv": "synthetic media"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Interruption"})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_scan_interrupt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic interruption write failure'; END $$; CREATE TRIGGER reject_scan_interrupt BEFORE UPDATE ON scan_run FOR EACH ROW WHEN(NEW.state='partial') EXECUTE FUNCTION reject_scan_interrupt()`)
	if err != nil {
		t.Fatal(err)
	}
	scanner := library.NewScanner(idgen.New(), partialReplacementFilesystem{rootfs.NewFilesystem()}, postgres.NewScanObservationStore(pool))
	_, err = scanner.ScanRoot(ctx, root.ID)
	if err == nil || !strings.Contains(err.Error(), "synthetic interruption write failure") {
		t.Fatal("write failure was discarded", err)
	}
	var state string
	var missing int64
	if err = pool.QueryRow(ctx, "SELECT state,files_missing_candidates FROM scan_run WHERE root_id=$1", root.ID).Scan(&state, &missing); err != nil || state != "running" || missing != 0 {
		t.Fatal(state, missing, err)
	}
	current, err := q.GetLibraryRootByID(ctx, root.ID)
	if err != nil || current.BaselineCompleted {
		t.Fatal("failed traversal published baseline", err)
	}
	if _, err = pool.Exec(ctx, "DROP TRIGGER reject_scan_interrupt ON scan_run"); err != nil {
		t.Fatal(err)
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, 2e9)
	defer cancel()
	report, err := library.NewScanner(idgen.New(), rootfs.NewFilesystem(), postgres.NewScanObservationStore(pool)).ScanRoot(recoveryCtx, root.ID)
	if err != nil || report.State != "succeeded" || report.FilesObserved != 1 {
		t.Fatal("physical slot or root did not recover", report, err)
	}
}
