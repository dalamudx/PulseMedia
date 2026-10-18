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
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	usecaseMedia "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestNativeTMDBTitleSearchRequiresCompleteUniqueMovie(t *testing.T) {
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
	filename := "Fight.Club.1999.1080p.BluRay.x264.mkv"
	file, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: gen.NewUUID(), RootID: root.ID, RelPath: filename, SizeBytes: 100, MtimeNs: time.Now().UnixNano()})
	if err != nil {
		t.Fatal(err)
	}
	lowConfidence := "Fight.Club.1080p.BluRay.x264.mkv"
	if _, err = q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: gen.NewUUID(), RootID: root.ID, RelPath: lowConfidence, SizeBytes: 100, MtimeNs: time.Now().UnixNano()}); err != nil {
		t.Fatal(err)
	}
	existing, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: gen.NewUUID(), Title: "Fight Club", OriginalTitle: pgtype.Text{String: "Fight Club", Valid: true}, Year: pgtype.Int4{Int32: 1999, Valid: true}, MediaType: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: gen.NewUUID(), MediaID: existing.ID, Namespace: "tmdb.movie", ExternalID: "550"}); err != nil {
		t.Fatal(err)
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
	complete := func(rows string, count int) string {
		return fmt.Sprintf(`{"page":1,"total_pages":1,"total_results":%d,"results":%s}`, count, rows)
	}
	known := `{"id":550,"title":"Fight Club","release_date":"1999-10-15"}`
	cases := []struct{ name, body string }{
		{"wrong_title", complete(`[{"id":999,"title":"A Different Movie","release_date":"1999-10-15"}]`, 1)},
		{"wrong_year", complete(`[{"id":550,"title":"Fight Club","release_date":"2000-10-15"}]`, 1)},
		{"same_title_and_year", complete(`[`+known+`,{"id":999,"title":"Fight Club","release_date":"1999-01-01"}]`, 2)},
		{"duplicate_identity", complete(`[`+known+`,`+known+`]`, 2)},
		{"unknown_year", complete(`[{"id":550,"title":"Fight Club"}]`, 1)},
		{"unknown_year_competitor", complete(`[`+known+`,{"id":999,"title":"Fight Club"}]`, 2)},
		{"zero_id", complete(`[{"id":0,"title":"Fight Club","release_date":"1999-10-15"}]`, 1)},
		{"wrong_type", complete(`[{"id":550,"media_type":"tv","title":"Fight Club","release_date":"1999-10-15"}]`, 1)},
		{"missing_pagination", `{"results":[` + known + `]}`},
		{"additional_page", `{"page":1,"total_pages":2,"total_results":21,"results":[` + known + `]}`},
		{"wrong_page", `{"page":2,"total_pages":2,"total_results":21,"results":[` + known + `]}`},
		{"inconsistent_count", complete(`[`+known+`]`, 2)},
		{"empty", `{"page":1,"total_pages":0,"total_results":0,"results":[]}`},
		{"missing_results", `{"page":1,"total_pages":1,"total_results":1}`},
		{"impossible_date", complete(`[{"id":550,"title":"Fight Club","release_date":"1999-99-99"}]`, 1)},
		{"unavailable", `{}`},
		{"unauthorized", `{}`},
		{"not_found", `{}`},
	}
	var mode, searches, other atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/movie" {
			other.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		searches.Add(1)
		if r.URL.Query().Get("query") != "Fight Club" || r.URL.Query().Get("year") != "1999" {
			t.Errorf("unexpected native search parameters: %s", r.URL.RawQuery)
		}
		if i := int(mode.Load()); i < len(cases) {
			switch cases[i].name {
			case "unavailable":
				w.WriteHeader(http.StatusServiceUnavailable)
			case "unauthorized":
				w.WriteHeader(http.StatusUnauthorized)
			case "not_found":
				w.WriteHeader(http.StatusNotFound)
			}
			_, _ = fmt.Fprint(w, cases[i].body)
			return
		}
		_, _ = fmt.Fprint(w, complete(`[{"id":999,"title":"Other Movie","release_date":"1999-01-01"},{"id":550,"title":"搏击俱乐部","original_title":"Fight Club","release_date":"1999-10-15"}]`, 2))
	}))
	defer server.Close()
	matcher := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(pool, gen), tmdb.NewClient("", tmdb.WithBaseURL(server.URL)))
	for i, tc := range cases {
		mode.Store(int32(i))

		result, err := matcher.MatchAndLinkFile(ctx, root.ID, filename)
		var class error
		switch tc.name {
		case "unavailable":
			class = ports.ErrUnavailable
		case "unauthorized":
			class = ports.ErrAuthRequired
		case "not_found":
			class = ports.ErrNotFound
		}
		if class != nil && !errors.Is(err, class) {
			t.Fatalf("%s lost external failure class: %v", tc.name, err)
		}
		if !errors.Is(err, usecaseMedia.ErrNoMetadataMatch) || result == nil || !result.RequiresReview || result.Metadata != nil {
			t.Fatalf("%s search bound uncertain movie: %#v err=%v", tc.name, result, err)
		}
		if snapshot() != before {
			t.Fatalf("%s search changed existing movie, file, mapping or fact", tc.name)
		}
	}
	calls := searches.Load()
	result, err := matcher.MatchAndLinkFile(ctx, root.ID, lowConfidence)
	if !errors.Is(err, usecaseMedia.ErrLowConfidence) || result == nil || !result.RequiresReview || searches.Load() != calls || snapshot() != before {
		t.Fatalf("low confidence submitted search or wrote: %#v err=%v", result, err)
	}
	mode.Store(int32(len(cases)))
	result, err = matcher.MatchAndLinkFile(ctx, root.ID, filename)
	if err != nil || result == nil || result.RequiresReview || result.Media.ID != existing.ID || result.Unit.ID != unit.ID || result.Metadata.TMDBID != 550 {
		t.Fatalf("unique original-title/year match failed: %#v err=%v", result, err)
	}
	var mediaCount, identities, units, linked int
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_identity),(SELECT count(*) FROM media_unit),(SELECT count(*) FROM library_file_unit l JOIN unit_fact f ON f.source_file_id=l.file_id AND f.unit_id=l.unit_id WHERE l.file_id=$1 AND l.unit_id=$2 AND f.stage='imported' AND f.revoked_at IS NULL)`, file.ID, unit.ID).Scan(&mediaCount, &identities, &units, &linked)
	if err != nil || mediaCount != 1 || identities != 1 || units != 1 || linked != 1 || other.Load() != 0 || searches.Load() != int32(len(cases)+1) {
		t.Fatalf("correct search created duplicate objects: %d/%d/%d/%d search/other=%d/%d err=%v", mediaCount, identities, units, linked, searches.Load(), other.Load(), err)
	}
}
