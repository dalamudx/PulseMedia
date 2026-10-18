package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	usecaseMedia "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type movieLinkProvider struct{ before func() }

func (p movieLinkProvider) GetMovieDetails(context.Context, int64, string) (*ports.MovieMetadata, error) {
	if p.before != nil {
		p.before()
	}
	return &ports.MovieMetadata{TMDBID: 550, IMDbID: "tt0137523", Title: "Fight Club", OriginalTitle: "Fight Club", Year: 1999, MediaType: "movie"}, nil
}
func (p movieLinkProvider) FindMovieByExternalID(ctx context.Context, _ string, _ string) (*ports.MovieMetadata, error) {
	return p.GetMovieDetails(ctx, 550, "")
}
func (p movieLinkProvider) SearchMovieIdentityCandidates(ctx context.Context, _ string, _ int) (ports.MovieIdentitySearch, error) {
	m, e := p.GetMovieDetails(ctx, 550, "")
	return ports.MovieIdentitySearch{Candidates: []*ports.MovieMetadata{m}, Complete: true}, e
}

type movieLinkFixture struct {
	pool       *pgxpool.Pool
	q          *db.Queries
	gen        ports.IDGen
	root, file uuid.UUID
	path       string
}

func newMovieLinkFixture(t *testing.T) movieLinkFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := postgres.Migrate(ctx, dsn, migrationsDir); e != nil {
		t.Fatal(e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	gen := idgen.New()
	q := postgres.Queries(pool)
	root, e := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: gen.NewUUID(), Path: t.TempDir(), MediaType: "movie", Name: "Movie link"})
	if e != nil {
		t.Fatal(e)
	}
	path := "Fight.Club.1999.{tmdb-550}.1080p.BluRay.x264.mkv"
	file, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: gen.NewUUID(), RootID: root.ID, RelPath: path, SizeBytes: 100, MtimeNs: time.Now().UnixNano()})
	if e != nil {
		t.Fatal(e)
	}
	return movieLinkFixture{pool, q, gen, root.ID, file.ID, path}
}
func TestMovieLinkReplayKeepsOneImportedFact(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx := context.Background()
	m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), movieLinkProvider{})
	first, e := m.MatchAndLinkFile(ctx, f.root, f.path)
	if e != nil {
		t.Fatal(e)
	}
	second, e := m.MatchAndLinkFile(ctx, f.root, f.path)
	if e != nil {
		t.Fatal(e)
	}
	if first.Media.ID != second.Media.ID || first.Unit.ID != second.Unit.ID {
		t.Fatal("replay changed identity")
	}
	var facts, seq int64
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE source_file_id=$1", f.file).Scan(&facts); e != nil {
		t.Fatal(e)
	}
	root, e := f.q.GetLibraryRootByID(ctx, f.root)
	if e != nil {
		t.Fatal(e)
	}
	seq = root.MutationSeq
	if facts != 1 || seq != 1 {
		t.Fatalf("replay appended state: facts=%d mutation=%d", facts, seq)
	}
}
func TestMovieLinkCannotAdoptReplacementDuringMetadata(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx := context.Background()
	replacement := f.gen.NewUUID()
	p := movieLinkProvider{before: func() {
		if e := f.q.RetireFileByID(ctx, f.file); e != nil {
			t.Fatal(e)
		}
		if _, e := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: replacement, RootID: f.root, RelPath: f.path, SizeBytes: 200, MtimeNs: time.Now().UnixNano()}); e != nil {
			t.Fatal(e)
		}
	}}
	m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), p)
	if _, e := m.MatchAndLinkFile(ctx, f.root, f.path); e == nil {
		t.Fatal("old metadata adopted replacement file")
	}
	var writes int
	if e := f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM media)+(SELECT count(*) FROM media_identity)+(SELECT count(*) FROM library_file_unit)+(SELECT count(*) FROM unit_fact)").Scan(&writes); e != nil {
		t.Fatal(e)
	}
	if writes != 0 {
		t.Fatalf("stale metadata wrote %d records", writes)
	}
}
