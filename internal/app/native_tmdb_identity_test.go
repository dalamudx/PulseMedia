package app_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	usecaseMedia "github.com/dalamudx/PulseMedia/internal/usecase/media"
)

func TestNativeTMDBWrongDetailsCannotLinkExistingMovie(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, migrationsDir); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	gen := idgen.New()
	q := postgres.Queries(pool)
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: gen.NewUUID(), Path: t.TempDir(), MediaType: "movie", Name: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	path := "Fight.Club.1999.{tmdb-550}.1080p.BluRay.x264.mkv"
	file, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: gen.NewUUID(), RootID: root.ID, RelPath: path, SizeBytes: 100, MtimeNs: time.Now().UnixNano()})
	if err != nil {
		t.Fatal(err)
	}
	var correct atomic.Bool
	var details, searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/550" {
			searches.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		details.Add(1)
		id := 999
		if correct.Load() {
			id = 550
		}
		_, _ = fmt.Fprintf(w, `{"id":%d,"title":"Fight Club","original_title":"Fight Club","release_date":"1999-10-15","overview":"Evidence"}`, id)
	}))
	defer server.Close()
	matcher := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(pool, gen), tmdb.NewClient("", tmdb.WithBaseURL(server.URL)))
	result, err := matcher.MatchAndLinkFile(ctx, root.ID, path)
	if !errors.Is(err, usecaseMedia.ErrNoMetadataMatch) || result == nil || !result.RequiresReview || result.Metadata != nil {
		t.Fatalf("wrong external identity gained link: %#v err=%v", result, err)
	}
	var mediaCount, identityCount, mappingCount, factCount int
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_identity),(SELECT count(*) FROM library_file_unit),(SELECT count(*) FROM unit_fact)`).Scan(&mediaCount, &identityCount, &mappingCount, &factCount)
	if err != nil || mediaCount != 0 || identityCount != 0 || mappingCount != 0 || factCount != 0 {
		t.Fatalf("invalid metadata wrote state: %d/%d/%d/%d err=%v", mediaCount, identityCount, mappingCount, factCount, err)
	}
	correct.Store(true)
	result, err = matcher.MatchAndLinkFile(ctx, root.ID, path)
	if err != nil || result.RequiresReview || result.Metadata.TMDBID != 550 || details.Load() != 2 || searches.Load() != 0 {
		t.Fatalf("same ID retry failed or searched: %#v err=%v calls=%d/%d", result, err, details.Load(), searches.Load())
	}
	var linked int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM library_file_unit l JOIN unit_fact f ON f.source_file_id=l.file_id AND f.unit_id=l.unit_id JOIN media_unit u ON u.id=l.unit_id JOIN media_identity i ON i.media_id=u.media_id WHERE l.file_id=$1 AND i.namespace='tmdb.movie' AND i.external_id='550' AND f.stage='imported' AND f.revoked_at IS NULL`, file.ID).Scan(&linked)
	if err != nil || linked != 1 {
		t.Fatalf("native adapter did not link correct existing file: %d err=%v", linked, err)
	}
}
