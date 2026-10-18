package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Date-only source evidence must not keep a previously accepted movie preview.
func TestSelectedMovieCalendarBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_UI_FIXTURE", "1")
	t.Setenv("PULSE_SELECTED_CALENDAR_BROWSER", "1")
	TestSelectedMovieUIFixture(t)
}

// Textual day/month/year evidence must not borrow the work's release year.
func TestSelectedMovieTextCalendarBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_TEXT_CALENDAR_BROWSER", "1")
	TestSelectedMovieCalendarBrowserHarness(t)
}

// Explicit collections must also invalidate a previously accepted movie preview.
func TestSelectedMovieCollectionBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_COLLECTION_BROWSER", "1")
	TestSelectedMovieCalendarBrowserHarness(t)
}

// A numbered multi-work suffix cannot keep the old movie preview or issue a write.
func TestSelectedMovieNumberedCollectionBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_NUMBERED_COLLECTION_BROWSER", "1")
	TestSelectedMovieCalendarBrowserHarness(t)
}

// Seven-work declarations and language tags cannot preserve an old movie preview.
func TestSelectedMovieLongNumberedCollectionBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_LONG_COLLECTION_BROWSER", "1")
	TestSelectedMovieCalendarBrowserHarness(t)
}

// A bare IMDb or paired-ID/relay suffix cannot hide a denied release group.
func TestSelectedMovieIdentityGroupBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	for _, variant := range []string{"identity", "hyphenated-tag", "bare-container"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("PULSE_SELECTED_IDENTITY_GROUP_BROWSER", "1")
			if variant == "bare-container" {
				t.Setenv("PULSE_SELECTED_BARE_CONTAINER_GROUP_BROWSER", "1")
			}
			if variant == "hyphenated-tag" {
				t.Setenv("PULSE_SELECTED_GROUP_TAG_DASH_BROWSER", "1")
			}
			TestSelectedMovieCalendarBrowserHarness(t)
		})
	}
}

// A post-quality numeric group never restores missing work-year coverage.
func TestSelectedMovieNumericGroupYearBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_NUMERIC_GROUP_YEAR_BROWSER", "1")
	TestSelectedMovieCalendarBrowserHarness(t)
}

// Local protocol/PG fixture only. Production never returns these source documents.
func TestSelectedMovieUIFixture(t *testing.T) {
	if os.Getenv("PULSE_SELECTED_UI_FIXTURE") != "1" {
		t.Skip("explicitly enabled browser fixture")
	}
	provider := &selectedHTTPMetadata{}
	a, adminSession, _, root, _, _, router, _ := setupOperationHTTP(t, func(h *httpapi.ServerHandler, a *app.App) {
		h.SetMovieSelectionService(acquisition.NewMovieSelectionService(postgres.NewAcquisitionStore(a.DBPool(), idgen.New(), clock.New(), acquisition.SelectMovieRound), provider, clock.New()))
	})
	ctx := context.Background()
	q := db.New(a.DBPool())
	if os.Getenv("PULSE_SELECTED_IDENTITY_GROUP_BROWSER") == "1" {
		rules, err := json.Marshal(quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}, RejectGroups: []string{"ASAP"}})
		if err != nil {
			t.Fatal(err)
		}
		// Publish a separate fixture revision; never overwrite frozen rules.
		if _, err = a.DBPool().Exec(ctx, "WITH published AS (INSERT INTO policy_revision(id,policy_id,revision,rules) VALUES($1,'default',2,$2) RETURNING policy_id) UPDATE policy SET current_revision=2 WHERE id IN (SELECT policy_id FROM published)", uuid.New(), rules); err != nil {
			t.Fatal(err)
		}
	}
	indexer, candidate, client, queryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, e := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexer, Name: "Browser protocol source", Url: "https://example.test", ApiKeyEncrypted: []byte("cipher"), Categories: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: indexer, Title: "Fight.Club.1999.1080p.Bluray.x264", Guid: "browser-selected", SourceConfigVersion: 1, SizeBytes: 1024, LinkEncrypted: []byte("cipher"), QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2","codec":"H264","codec_parser_version":"codec.tokens.v2"}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "Browser chosen client", ClientType: "qbittorrent", Url: "http://127.0.0.1:8080", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.DBPool().Exec(ctx, "INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','Fight Club',1,'succeeded',clock_timestamp())", queryID, indexer); e != nil {
		t.Fatal(e)
	}
	if e := q.LinkQueryCandidate(ctx, db.LinkQueryCandidateParams{QueryID: queryID, CandidateID: candidate}); e != nil {
		t.Fatal(e)
	}
	stopFixture := make(chan struct{})
	var stopOnce sync.Once
	var evidenceMu sync.Mutex
	var keys []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/stop":
			stopOnce.Do(func() { close(stopFixture) })
			w.WriteHeader(200)
			return
		case "/api/v1/acquisitions/selected-movie":
			if r.Method == http.MethodPost {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, r)
				evidenceMu.Lock()
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				first := len(keys) == 1
				evidenceMu.Unlock()
				if first && rec.Code == 202 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"code":"FIXTURE_LOST_RECEIPT","message":"result not confirmed"}`))
					return
				}
				for name, values := range rec.Header() {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.WriteHeader(rec.Code)
				_, _ = w.Write(rec.Body.Bytes())
				return
			}
		case "/api/v1/discover":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"tmdb_id": 550, "media_type": "movie", "title": "Fight Club", "year": 1999, "overview": "Local metadata protocol fixture, real PostgreSQL command path", "genres": []string{"剧情"}}}})
			return
		case "/api/v1/releases/search":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": candidate, "title": "Fight.Club.1999.1080p.Bluray.x264", "indexer_name": "Browser protocol source", "size_bytes": 1024, "seeders": 1}}})
			return
		case "/fixture/user-active":
			if os.Getenv("PULSE_SELECTED_USER_BROWSER") != "1" || r.Method != http.MethodPost {
				w.WriteHeader(404)
				return
			}
			var body struct {
				Active *bool `json:"active"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Active == nil {
				w.WriteHeader(400)
				return
			}
			if _, e := a.DBPool().Exec(r.Context(), "UPDATE app_user SET is_active=$1 WHERE username='admin'", *body.Active); e != nil {
				t.Error(e)
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(204)
			return
		case "/fixture/calendar-title":
			if os.Getenv("PULSE_SELECTED_CALENDAR_BROWSER") != "1" || r.Method != http.MethodPost {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			rawTitle := "Fight.Club.1999.01.02.1080p.Bluray.x264"
			if os.Getenv("PULSE_SELECTED_SEASON_SCOPE_BROWSER") == "1" {
				rawTitle = "Fight.Club.1999.S01.1080p.Bluray.x264.{tmdb-550}"
			}
			if os.Getenv("PULSE_SELECTED_FRENCH_COLLECTION_BROWSER") == "1" {
				rawTitle = "Fight.Club.1999.INTEGRALE.MULTI.1080p.Bluray.x264"
			}
			if os.Getenv("PULSE_SELECTED_RIP_TAIL_YEAR_BROWSER") == "1" {
				rawTitle = "Fight.Club.DVDRip.1999.1080p.x264"
			}
			if os.Getenv("PULSE_SELECTED_TEXT_CALENDAR_BROWSER") == "1" {
				rawTitle = "Fight.Club.3rd.Nov.1999.1080p.Bluray.x264"
			}
			if os.Getenv("PULSE_SELECTED_NUMBERED_COLLECTION_BROWSER") == "1" {
				rawTitle = "Fight.Club.Quadrilogy.1999.1080p.Bluray.x264"
			}
			if os.Getenv("PULSE_SELECTED_LONG_COLLECTION_BROWSER") == "1" {
				rawTitle = "Fight.Club.Heptalogy.SWESUB.1999.1080p.Bluray.x264"
			}
			if os.Getenv("PULSE_SELECTED_COLLECTION_BROWSER") == "1" {
				rawTitle = "Fight.Club.Complete.Collection.1999.1080p.Bluray.x264"
			}
			if os.Getenv("PULSE_SELECTED_IDENTITY_GROUP_BROWSER") == "1" {
				rawTitle = "Fight.Club.1999.1080p.Bluray.x264-ASAP.imdb-tt0137523.mkv"
				if os.Getenv("PULSE_SELECTED_BARE_CONTAINER_GROUP_BROWSER") == "1" {
					rawTitle = "Fight.Club.1999.1080p.Bluray.x264-ASAP mkv"
				}
				if os.Getenv("PULSE_SELECTED_GROUP_TAG_DASH_BROWSER") == "1" {
					rawTitle = "Fight.Club.1999.1080p.Bluray.x264-ASAP[unreviewed-other].imdb-tt0137523.mkv"
				}
			}
			if os.Getenv("PULSE_SELECTED_NUMERIC_GROUP_YEAR_BROWSER") == "1" {
				rawTitle = "Fight.Club.1080p.Bluray.x264-1920.mkv"
			}
			if _, err := a.DBPool().Exec(ctx, "UPDATE release_candidate SET title=$2 WHERE id=$1", candidate, rawTitle); err != nil {
				t.Error(err)
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/fixture/calendar-counts":
			if os.Getenv("PULSE_SELECTED_CALENDAR_BROWSER") != "1" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var counts [6]int
			if err := a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM idempotency_record),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM download_task),(SELECT count(*) FROM job WHERE kind IN ('download_submission','episode_download_submission'))").Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5]); err != nil {
				t.Error(err)
				w.WriteHeader(503)
				return
			}
			var profile string
			if err := a.DBPool().QueryRow(ctx, "SELECT quality_profile::text FROM release_candidate WHERE id=$1", candidate).Scan(&profile); err != nil {
				t.Error(err)
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"counts": counts, "quality": profile, "root": root, "client": client})
			return
		case "/fixture/selection-counts":
			var jobs, entities int
			_ = a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='download_submission'),(SELECT count(*) FROM media_identity WHERE namespace='tmdb.movie' AND external_id='550')").Scan(&jobs, &entities)
			evidenceMu.Lock()
			writtenKeys := append([]string(nil), keys...)
			evidenceMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": writtenKeys, "jobs": jobs, "entities": entities, "meta_calls": provider.calls.Load(), "root": root, "client": client})
			return
		}
		router.ServeHTTP(w, r)
	})
	address := "127.0.0.1:8188"
	if os.Getenv("PULSE_SELECTED_CALENDAR_BROWSER") == "1" {
		address = "127.0.0.1:0"
	}
	listener, e := net.Listen("tcp", address)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(stop); err != nil {
			t.Error(err)
		}
		select {
		case <-done:
		case <-stop.Done():
			t.Error("fixture server not joined")
		}
	})
	base := "http://" + listener.Addr().String()
	t.Log("SELECTED_UI_READY " + base)
	if os.Getenv("PULSE_SELECTED_CALENDAR_BROWSER") == "1" {
		scriptName := "../../web/tests/selected-movie-calendar-browser.mjs"
		if os.Getenv("PULSE_SELECTED_USER_BROWSER") == "1" {
			scriptName = "../../web/tests/selected-movie-user-browser.mjs"
		}
		script, err := filepath.Abs(scriptName)
		if err != nil {
			t.Fatal(err)
		}
		browserCtx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
		defer cancel()
		cmd := exec.CommandContext(browserCtx, "node", script)
		cmd.Env = append(os.Environ(), "PULSE_SELECTED_CALENDAR_BASE="+base)
		if os.Getenv("PULSE_SELECTED_USER_BROWSER") == "1" {
			cmd.Env = append(cmd.Env, "PULSE_SELECTED_BROWSER_SESSION="+adminSession)
		}
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("calendar browser failed: %v %s", err, raw)
		}
		t.Log(string(raw))
		return
	}

	select {
	case <-stopFixture:
	case <-time.After(40 * time.Minute):
	case e := <-done:
		t.Fatal(e)
	}
}

// French paired package markers clear an old movie preview without releasing facts.
func TestSelectedMovieFrenchCollectionBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_FRENCH_COLLECTION_BROWSER", "1")
	TestSelectedMovieCalendarBrowserHarness(t)
}

// A work year inside a technical rip tail cannot preserve old movie permission.
func TestSelectedMovieRipTailYearBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_RIP_TAIL_YEAR_BROWSER", "1")
	TestSelectedMovieCalendarBrowserHarness(t)
}

// A season scope cannot keep an old movie preview or create an acquisition.
func TestSelectedMovieSeasonScopeBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SELECTED_SEASON_SCOPE_BROWSER", "1")
	TestSelectedMovieCalendarBrowserHarness(t)
}
