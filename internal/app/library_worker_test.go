package app

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func waitNativeScan(t *testing.T, check func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ok, err := check()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("production scan did not reach expected persisted state")
}
func TestAppRunOwnsLibraryScanAndMissingAfterRestart(t *testing.T) {
	observer, dsn := bootstrapTestPool(t)
	ctx := context.Background()
	data := t.TempDir()
	if err := ensureDefaultLibraryRoots(ctx, observer, idgen.New(), data); err != nil {
		t.Fatal(err)
	}
	q := postgres.Queries(observer)
	movie, err := q.GetFirstOnlineRootForType(ctx, "movie")
	if err != nil {
		t.Fatal(err)
	}
	tv, err := q.GetFirstOnlineRootForType(ctx, "tv")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(movie.Path, "Observed.mkv")
	if err = os.WriteFile(path, []byte("native library contents"), 0600); err != nil {
		t.Fatal(err)
	}
	_, release, err := rootfs.NewFilesystem().LockRoots(ctx, tv.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := func() func() {
		pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
		if e != nil {
			t.Fatal(e)
		}
		cfg := config.NewDefault()
		cfg.DataDir = data
		cfg.Database.URL = dsn
		cfg.SecretKey = hex.EncodeToString(outboxTestKey)
		cfg.Server.HTTPAddr = "127.0.0.1:0"
		a, e := New(cfg, WithDBPool(pool), WithLogger(logging.NewLogger(io.Discard, "error")))
		if e != nil {
			pool.Close()
			t.Fatal(e)
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- a.Run(runCtx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case e := <-done:
					if e != nil && !errors.Is(e, context.Canceled) {
						t.Error(e)
					}
				case <-time.After(10 * time.Second):
					t.Error("scan worker not drained")
				}
				if e := a.Close(); e != nil {
					t.Error(e)
				}
				s := a.stages.snapshot()[ports.StageLibraryScan]
				if s.InFlight != 0 || s.Returned == 0 || s.Seconds <= 0 {
					t.Errorf("scan calls not drained/observed: %+v", s)
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	stop := start()
	waitNativeScan(t, func() (bool, error) {
		var ready bool
		var busy int
		e := observer.QueryRow(ctx, `SELECT baseline_completed,(SELECT count(*) FROM library_scan_job l JOIN job j ON j.id=l.job_id WHERE l.root_id=$2 AND j.state='running') FROM library_root WHERE id=$1`, movie.ID, tv.ID).Scan(&ready, &busy)
		return ready && busy == 1, e
	})
	slow, err := q.GetLibraryRootByID(ctx, tv.ID)
	if err != nil || slow.BaselineCompleted {
		t.Fatal("blocked root falsely scanned", err)
	}
	release()
	waitNativeScan(t, func() (bool, error) {
		var n int
		e := observer.QueryRow(ctx, "SELECT count(*) FROM library_root WHERE baseline_completed=false").Scan(&n)
		return n == 0, e
	})
	file, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: movie.ID, RelPath: "Observed.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	media, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "Synthetic prior library fact", MediaType: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: media.ID, UnitKey: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: unit.ID}); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"imported", "confirmed"} {
		if _, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), RootID: pgtype.UUID{Bytes: movie.ID, Valid: true}, UnitID: unit.ID, Stage: stage, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, QualityProfile: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitNativeScan(t, func() (bool, error) {
		var n int
		e := observer.QueryRow(ctx, "SELECT count(*) FROM library_scan_job l JOIN job j ON j.id=l.job_id WHERE j.state IN('queued','running')").Scan(&n)
		return n == 0, e
	})
	if _, err = observer.Exec(ctx, "UPDATE library_scan_schedule SET next_run_at=clock_timestamp() WHERE root_id=$1", movie.ID); err != nil {
		t.Fatal(err)
	}
	var firstMissing uuid.UUID
	waitNativeScan(t, func() (bool, error) {
		var n int
		e := observer.QueryRow(ctx, `SELECT count(*) FROM missing_candidate c JOIN scan_run r ON r.id=c.last_run_id JOIN library_scan_job l ON l.root_id=c.root_id AND l.run_id=r.id AND l.phase='missing' JOIN job j ON j.id=l.job_id WHERE c.file_id=$1 AND c.state='open' AND j.state='succeeded'`, file.ID).Scan(&n)
		return n == 1, e
	})
	if err = observer.QueryRow(ctx, "SELECT first_run_id FROM missing_candidate WHERE file_id=$1", file.ID).Scan(&firstMissing); err != nil {
		t.Fatal(err)
	}
	stop()
	// Seed the previous completed source window; the restarted App executes the
	// second traversal and native NotExist checks, not a simulated missing result.
	if _, err = observer.Exec(ctx, "UPDATE scan_run SET created_at=created_at-interval '7 minutes',finished_at=finished_at-interval '6 minutes' WHERE id=$1", firstMissing); err != nil {
		t.Fatal(err)
	}
	if _, err = observer.Exec(ctx, "UPDATE scan_finalization SET completed_at=completed_at-interval '6 minutes' WHERE scan_run_id=$1", firstMissing); err != nil {
		t.Fatal(err)
	}
	if _, err = observer.Exec(ctx, "UPDATE library_scan_schedule SET next_run_at=clock_timestamp() WHERE root_id=$1", movie.ID); err != nil {
		t.Fatal(err)
	}
	stop2 := start()
	waitNativeScan(t, func() (bool, error) {
		var retired bool
		var invalid, receipt, success int
		e := observer.QueryRow(ctx, `SELECT retired_at IS NOT NULL,(SELECT count(*) FROM unit_fact WHERE source_file_id=$1 AND revoked_at IS NOT NULL),(SELECT count(*) FROM scan_missing_receipt WHERE file_id=$1),(SELECT count(*) FROM library_scan_job l JOIN job j ON j.id=l.job_id JOIN scan_missing_receipt m ON m.last_run_id=l.run_id WHERE m.file_id=$1 AND l.phase='missing' AND j.state='succeeded') FROM library_file WHERE id=$1`, file.ID).Scan(&retired, &invalid, &receipt, &success)
		return retired && invalid == 2 && receipt == 1 && success == 1, e
	})
	stop2()
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scan altered source filesystem", err)
	}
	var n int
	if err = observer.QueryRow(ctx, "SELECT count(*) FROM scan_missing_receipt WHERE file_id=$1", file.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
