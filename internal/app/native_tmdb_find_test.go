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
	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	usecaseMedia "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestNativeTMDBExternalFindCannotBindUncertainMovie(t *testing.T) {
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
	filename := "Fight.Club.1999.imdb-tt0137523.1080p.BluRay.x264.mkv"
	file, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: gen.NewUUID(), RootID: root.ID, RelPath: filename, SizeBytes: 100, MtimeNs: time.Now().UnixNano()})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: gen.NewUUID(), Title: "Fight Club", OriginalTitle: pgtype.Text{String: "Fight Club", Valid: true}, Year: pgtype.Int4{Int32: 1999, Valid: true}, MediaType: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []struct{ namespace, id string }{{"tmdb.movie", "550"}, {"imdb", "tt0137523"}} {
		if _, err := q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: gen.NewUUID(), MediaID: existing.ID, Namespace: identity.namespace, ExternalID: identity.id}); err != nil {
			t.Fatal(err)
		}
	}
	unit, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: gen.NewUUID(), MediaID: existing.ID, UnitKey: domainMedia.FormatMovieUnitKey()})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var v string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object('media',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM media m),'identities',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM media_identity i),'units',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM media_unit u),'files',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM library_file f),'root',(SELECT to_jsonb(r) FROM library_root r WHERE id=$1),'mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY file_id,unit_id) FROM library_file_unit m),'facts',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM unit_fact f))::text`, root.ID).Scan(&v)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := snapshot()
	var mode, finds, details, searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/find/tt0137523" {
			finds.Add(1)
			switch mode.Load() {
			case 0:
				_, _ = fmt.Fprint(w, `{"movie_results":[{"id":550},{"id":999}]}`)
			case 1:
				_, _ = fmt.Fprint(w, `{"movie_results":[]}`)
			case 2:
				_, _ = fmt.Fprint(w, `{"movie_results":[{"id":0}]}`)
			case 3:
				_, _ = fmt.Fprint(w, `{"movie_results":[{"id":999}]}`)
			default:
				_, _ = fmt.Fprint(w, `{"movie_results":[{"id":550}]}`)
			}
			return
		}
		switch r.URL.Path {
		case "/movie/999":
			details.Add(1)
			_, _ = fmt.Fprint(w, `{"id":999,"imdb_id":"tt0000001","title":"Different movie","release_date":"1999-01-01","overview":"Evidence"}`)
		case "/movie/550":
			details.Add(1)
			_, _ = fmt.Fprint(w, `{"id":550,"imdb_id":"tt0137523","title":"Fight Club","release_date":"1999-10-15","overview":"Evidence"}`)
		default:
			searches.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	matcher := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(pool, gen), tmdb.NewClient("", tmdb.WithBaseURL(server.URL)))
	for _, v := range []int32{0, 1, 2, 3} {
		mode.Store(v)
		result, err := matcher.MatchAndLinkFile(ctx, root.ID, filename)
		if !errors.Is(err, usecaseMedia.ErrNoMetadataMatch) || result == nil || !result.RequiresReview || result.Metadata != nil {
			t.Fatalf("uncertain Find mode %d bound movie: %#v err=%v", v, result, err)
		}
		if snapshot() != before {
			t.Fatalf("uncertain Find mode %d changed existing movie/file/fact state", v)
		}
	}
	if details.Load() != 1 || searches.Load() != 0 {
		t.Fatalf("rejected Find performed fallback: details=%d searches=%d", details.Load(), searches.Load())
	}
	mode.Store(4)
	result, err := matcher.MatchAndLinkFile(ctx, root.ID, filename)
	if err != nil || result == nil || result.RequiresReview || result.Media.ID != existing.ID || result.Unit.ID != unit.ID {
		t.Fatalf("unique corrected Find failed original movie association: %#v err=%v", result, err)
	}
	var mediaCount, identities, units, linked int
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_identity),(SELECT count(*) FROM media_unit),(SELECT count(*) FROM library_file_unit l JOIN unit_fact f ON f.source_file_id=l.file_id AND f.unit_id=l.unit_id WHERE l.file_id=$1 AND l.unit_id=$2 AND f.stage='imported' AND f.revoked_at IS NULL)`, file.ID, unit.ID).Scan(&mediaCount, &identities, &units, &linked)
	if err != nil || mediaCount != 1 || identities != 2 || units != 1 || linked != 1 || finds.Load() != 5 || details.Load() != 2 || searches.Load() != 0 {
		t.Fatalf("identity association counts %d/%d/%d/%d find/details/search=%d/%d/%d err=%v", mediaCount, identities, units, linked, finds.Load(), details.Load(), searches.Load(), err)
	}
}
