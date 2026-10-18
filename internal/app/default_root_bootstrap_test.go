package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func bootstrapTestPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	p, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, dsn
}

func TestDefaultRootBootstrapCannotClaimUnscannedBaseline(t *testing.T) {
	p, _ := bootstrapTestPool(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := ensureDefaultLibraryRoots(ctx, p, idgen.New(), dir); err != nil {
		t.Fatal(err)
	}
	var total, unscanned int
	if err := p.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE NOT baseline_completed) FROM library_root").Scan(&total, &unscanned); err != nil {
		t.Fatal(err)
	}
	if total != 2 || unscanned != 2 {
		t.Fatalf("bootstrap fabricated scan: roots=%d unscanned=%d", total, unscanned)
	}
}

func TestDefaultRootBootstrapRejectsAliasesWithoutOverwrite(t *testing.T) {
	for _, kind := range []string{"media-link", "movie-link", "marker-link", "marker-hardlink", "marker-fifo"} {
		t.Run(kind, func(t *testing.T) {
			p, _ := bootstrapTestPool(t)
			ctx := context.Background()
			dir, outside := t.TempDir(), t.TempDir()
			protected := filepath.Join(outside, "protected")
			original := []byte("protected user-owned bytes")
			if err := os.WriteFile(protected, original, 0600); err != nil {
				t.Fatal(err)
			}
			movie := filepath.Join(dir, "media", "movies")
			if err := os.MkdirAll(movie, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(movie, ".pulsemedia_root")
			switch kind {
			case "media-link":
				if err := os.RemoveAll(filepath.Join(dir, "media")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, "media")); err != nil {
					t.Fatal(err)
				}
			case "movie-link":
				if err := os.Remove(movie); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, movie); err != nil {
					t.Fatal(err)
				}
			case "marker-link":
				if err := os.Symlink(protected, marker); err != nil {
					t.Fatal(err)
				}
			case "marker-hardlink":
				if err := os.Link(protected, marker); err != nil {
					t.Fatal(err)
				}
			case "marker-fifo":
				makeBootstrapFIFO(t, marker)
			}
			err := ensureDefaultLibraryRoots(ctx, p, idgen.New(), dir)
			after, e := os.ReadFile(protected)
			if e != nil || string(after) != string(original) {
				t.Fatalf("bootstrap overwrote protected target: %v", e)
			}
			if err == nil {
				t.Fatal("unsafe alias accepted")
			}
			var n int
			if e := p.QueryRow(ctx, "SELECT count(*) FROM library_root").Scan(&n); e != nil || n != 0 {
				t.Fatalf("unsafe bootstrap persisted roots: %d %v", n, e)
			}
		})
	}
}

func TestDefaultRootBootstrapDMLFailureRollsBackBothRoots(t *testing.T) {
	p, _ := bootstrapTestPool(t)
	ctx := context.Background()
	_, err := p.Exec(ctx, `CREATE FUNCTION reject_bootstrap_tv() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.media_type='tv' THEN RAISE EXCEPTION 'owned bootstrap failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_bootstrap_tv BEFORE INSERT ON library_root FOR EACH ROW EXECUTE FUNCTION reject_bootstrap_tv()`)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err = ensureDefaultLibraryRoots(ctx, p, idgen.New(), dir)
	var n int
	if e := p.QueryRow(ctx, "SELECT count(*) FROM library_root").Scan(&n); e != nil {
		t.Fatal(e)
	}
	if err == nil || n != 0 {
		t.Fatalf("failure ignored or partial root commit: err=%v roots=%d", err, n)
	}
	if _, err = p.Exec(ctx, "DROP TRIGGER reject_bootstrap_tv ON library_root"); err != nil {
		t.Fatal(err)
	}
	if err = ensureDefaultLibraryRoots(ctx, p, idgen.New(), dir); err != nil {
		t.Fatalf("retry after DB failure: %v", err)
	}
}

func TestDefaultRootFailureStopsAppBeforeHTTP(t *testing.T) {
	_, dsn := bootstrapTestPool(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "media"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.DataDir = dir
	cfg.Database.URL = dsn
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, err := New(cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err = a.Run(ctx); err == nil {
		t.Fatal("startup ignored root failure")
	}
	if !strings.Contains(err.Error(), "failed to initialize library roots") {
		t.Fatalf("wrong startup rejection: %v", err)
	}
	if a.Readyz(context.Background()).Ready {
		t.Fatal("failed bootstrap reported ready")
	}
}
