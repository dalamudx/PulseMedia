package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
)

func TestDefaultRootsRequireRealScanAndUseInjectedID(t *testing.T) {
	pool, _ := bootstrapTestPool(t)
	ctx := context.Background()
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	gen := idgen.NewMock(ids...)
	c := clock.NewMock(time.Date(2030, 2, 3, 4, 5, 6, 0, time.UTC))
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, WithClock(c), WithIDGen(gen), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	if err = ensureDefaultLibraryRoots(ctx, pool, a.IDGen(), a.cfg.DataDir); err != nil {
		t.Fatal(err)
	}
	if !a.Clock().Now().Equal(c.Now()) {
		t.Fatal("clock injection lost")
	}
	q := postgres.Queries(pool)
	for i, kind := range []string{"movie", "tv"} {
		root, err := q.GetFirstOnlineRootForType(ctx, kind)
		if err != nil || root.ID != ids[i] || root.BaselineCompleted {
			t.Fatalf("injected ID or baseline mismatch: %+v %v", root, err)
		}
		if _, err = library.NewScanner(idgen.New(), rootfs.NewFilesystem(), postgres.NewScanObservationStore(pool)).ScanRoot(ctx, root.ID); err != nil {
			t.Fatal(err)
		}
		updated, err := q.GetLibraryRootByID(ctx, root.ID)
		if err != nil || !updated.BaselineCompleted {
			t.Fatalf("actual scan did not establish baseline: %v", err)
		}
	}
}

func TestDefaultRootBootstrapPreservesOfflineRootsAndMarkers(t *testing.T) {
	pool, _ := bootstrapTestPool(t)
	ctx := context.Background()
	q := postgres.Queries(pool)
	data := t.TempDir()
	protected := []byte("user configured marker")
	for _, kind := range []string{"movie", "tv"} {
		path := filepath.Join(t.TempDir(), kind)
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), protected, 0600); err != nil {
			t.Fatal(err)
		}
		root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: kind, Name: "protected existing root"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, "UPDATE library_root SET health='offline',generation=7 WHERE id=$1", root.ID); err != nil {
			t.Fatal(err)
		}
	}
	var before string
	if err := pool.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(r) ORDER BY id)::text FROM library_root r").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "media"), []byte("must not touch"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDefaultLibraryRoots(ctx, pool, idgen.NewMock(), data); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := pool.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(r) ORDER BY id)::text FROM library_root r").Scan(&after); err != nil || after != before {
		t.Fatalf("offline history altered: %v", err)
	}
}

func TestDefaultRootBootstrapEightConcurrentRegistrations(t *testing.T) {
	pool, _ := bootstrapTestPool(t)
	ctx := context.Background()
	repo := postgres.NewLibraryBootstrapStore(pool)
	data := t.TempDir()
	fs := rootfs.NewFilesystem()
	paths := map[string]string{}
	for _, kind := range []string{"movie", "tv"} {
		path, err := fs.PrepareDefaultRoot(ctx, data, kind)
		if err != nil {
			t.Fatal(err)
		}
		paths[kind] = path
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- repo.RegisterInitialRoots(ctx, []ports.InitialLibraryRoot{{ID: uuid.New(), Path: paths["movie"], MediaType: "movie", Name: "Movies"}, {ID: uuid.New(), Path: paths["tv"], MediaType: "tv", Name: "TV"}})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n, baseline int
	if err := pool.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE baseline_completed) FROM library_root").Scan(&n, &baseline); err != nil || n != 2 || baseline != 0 {
		t.Fatalf("concurrent registration: roots=%d baseline=%d err=%v", n, baseline, err)
	}
}
