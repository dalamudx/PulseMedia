package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type m5CatalogReadApp struct {
	*app.App
	reader ports.LocalMediaSearchReader
}

func (a *m5CatalogReadApp) LocalMediaSearchReader() ports.LocalMediaSearchReader { return a.reader }
func TestM5MetadataDiscoveryHTTPPG(t *testing.T) {
	a, authService, bootstrap := setupAuthHTTP(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("api_key") != "m5-fixture-key" {
			t.Error("missing configured metadata credential")
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/tv/") {
			switch r.URL.Path {
			case "/tv/21":
				_, _ = w.Write([]byte(`{"id":21,"name":"Series","first_air_date":"2024-01-01","number_of_seasons":2,"number_of_episodes":3,"seasons":[{"season_number":0,"episode_count":4},{"season_number":1,"episode_count":2},{"season_number":2,"episode_count":1}]}`))
			case "/tv/404":
				w.WriteHeader(404)
			case "/tv/500":
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"secret":"upstream-error-must-not-leak"}`))
			default:
				_, _ = w.Write([]byte(`{"id":999,"name":"wrong identity","seasons":[]}`))
			}
			return
		}
		switch r.URL.Query().Get("query") {
		case "empty":
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		case "broken":
			_, _ = w.Write([]byte(`{"message":"upstream-error-must-not-leak"}`))
			return
		case "offline":
			w.WriteHeader(500)
			return
		case "rate":
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		case "local":
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/search/movie":
			if r.URL.Query().Get("year") != "2023" {
				t.Error("movie year was not forwarded")
			}
			_, _ = w.Write([]byte(`{"results":[{"id":11,"title":"Movie","release_date":"2023-01-01"}]}`))
		case "/search/tv":
			if r.URL.Query().Get("first_air_date_year") != "2024" {
				t.Error("TV first-air year was not forwarded")
			}
			_, _ = w.Write([]byte(`{"results":[{"id":21,"name":"Series","first_air_date":"2024-01-01"}]}`))
		case "/search/multi":
			_, _ = w.Write([]byte(`{"results":[{"id":11,"media_type":"movie","title":"Movie","release_date":"2023-01-01"},{"id":21,"media_type":"tv","name":"Series","first_air_date":"2024-01-01"},{"id":31,"media_type":"person","name":"Person"}]}`))
		default:
			t.Errorf("unexpected metadata route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{"username":"metadata_admin","password":"Metadata-fixture-only-8274!"}`))
	setup.Header.Set("Content-Type", "application/json")
	setup.RemoteAddr = "127.0.0.1:1234"
	setup.Host = "localhost"
	initialized := httptest.NewRecorder()
	bootstrap.ServeHTTP(initialized, setup)
	if initialized.Code != 200 {
		t.Fatalf("setup status %d", initialized.Code)
	}
	cookies := initialized.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("missing real session")
	}
	client := tmdb.NewClient("m5-fixture-key", tmdb.WithBaseURL(upstream.URL), tmdb.WithHTTPClient(&http.Client{Timeout: time.Second}))
	handler := httpapi.NewServerHandler(a, authService, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(a, handler, authService, nil)
	get := func(path string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			for _, cookie := range cookies {
				request.AddCookie(cookie)
			}
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if bytes.Contains(response.Body.Bytes(), []byte("m5-fixture-key")) || bytes.Contains(response.Body.Bytes(), []byte("upstream-error-must-not-leak")) {
			t.Fatal("metadata secret leaked")
		}
		return response
	}
	before := calls.Load()
	if response := get("/api/v1/search/movies?query=movie", false); response.Code != 401 {
		t.Fatalf("missing session: %d", response.Code)
	}
	if calls.Load() != before {
		t.Fatal("unauthenticated metadata I/O")
	}
	for _, test := range []struct {
		kind  string
		year  int
		count int
		first string
	}{{"movie", 2023, 1, "movie"}, {"tv", 2024, 1, "tv"}, {"multi", 0, 2, "movie"}, {"all", 0, 2, "movie"}, {"multi", 2024, 1, "tv"}} {
		t.Run(fmt.Sprintf("%s-%d", test.kind, test.year), func(t *testing.T) {
			path := "/api/v1/search/movies?query=match&type=" + test.kind
			if test.year > 0 {
				path += fmt.Sprintf("&year=%d", test.year)
			}
			response := get(path, true)
			if response.Code != 200 {
				t.Fatalf("search status %d: %s", response.Code, response.Body.String())
			}
			var body httpapi.MovieSearchResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Results) != test.count || body.Results[0].MediaType == nil || string(*body.Results[0].MediaType) != test.first {
				t.Fatal("wrong normalized search results", body.Results)
			}
		})
	}
	for _, kind := range []string{"movie", "tv", "multi"} {
		response := get("/api/v1/search/movies?query=empty&type="+kind, true)
		var body httpapi.MovieSearchResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 || body.Results == nil || len(body.Results) != 0 {
			t.Fatalf("empty search must be 200 []: %d %s", response.Code, response.Body.String())
		}
	}
	before = calls.Load()
	for _, query := range []string{"query=+", "query=x&type=person", "query=x&year=0", "query=x&year=1700"} {
		if response := get("/api/v1/search/movies?"+query, true); response.Code != 400 {
			t.Fatalf("invalid query %q returned %d", query, response.Code)
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid search reached provider")
	}
	for _, query := range []string{"broken", "offline"} {
		if response := get("/api/v1/search/movies?query="+query, true); response.Code != 503 {
			t.Fatalf("provider %s: %d", query, response.Code)
		}
	}
	response := get("/api/v1/media/tmdb/tv/21", true)
	var tv httpapi.TMDBTVDetailsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &tv); err != nil || response.Code != 200 || tv.SeasonsCount != 2 || tv.EpisodesCount != 3 || len(tv.Seasons) != 3 || tv.Seasons[0].EpisodeCount != 4 {
		t.Fatalf("TV summary: %d %s", response.Code, response.Body.String())
	}
	for _, test := range []struct {
		id     int
		status int
	}{{404, 404}, {500, 503}, {22, 503}} {
		if response := get(fmt.Sprintf("/api/v1/media/tmdb/tv/%d", test.id), true); response.Code != test.status {
			t.Fatalf("TV %d returned %d", test.id, response.Code)
		}
	}
	// The local fallback is real PG, preserves movie/TV type and a trustworthy ID,
	// and never turns a database failure into an apparently empty remote search.
	movie, series := uuid.New(), uuid.New()
	for _, entry := range []struct {
		id       uuid.UUID
		kind     string
		year     int
		external string
	}{{movie, "movie", 2023, "101"}, {series, "tv", 2024, "202"}} {
		if _, err := a.DBPool().Exec(t.Context(), "INSERT INTO media(id,media_type,title,year) VALUES($1,$2,'local',$3)", entry.id, entry.kind, entry.year); err != nil {
			t.Fatal(err)
		}
		if _, err := a.DBPool().Exec(t.Context(), "INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,$3,$4)", uuid.New(), entry.id, "tmdb."+entry.kind, entry.external); err != nil {
			t.Fatal(err)
		}
	}
	// A second alias cannot duplicate the same media in the fallback.
	if _, err := a.DBPool().Exec(t.Context(), "INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','102')", uuid.New(), movie); err != nil {
		t.Fatal(err)
	}
	response = get("/api/v1/search/movies?query=local&type=tv&year=2024", true)
	var local httpapi.MovieSearchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &local); err != nil || response.Code != 200 || len(local.Results) != 1 || local.Results[0].TmdbId != 202 || *local.Results[0].MediaType != "tv" {
		t.Fatalf("local fallback %d %s", response.Code, response.Body.String())
	}
	response = get("/api/v1/search/movies?query=local&type=multi", true)
	if err := json.Unmarshal(response.Body.Bytes(), &local); err != nil || response.Code != 200 || len(local.Results) != 2 {
		t.Fatalf("local fallback alias duplicates: %d %s", response.Code, response.Body.String())
	}
	var actor *ports.Actor
	for _, cookie := range cookies {
		if cookie.HttpOnly {
			var err error
			actor, err = authService.AuthenticateToken(t.Context(), cookie.Value)
			if err != nil {
				t.Fatal("session could not be revalidated")
			}
			break
		}
	}
	if actor == nil {
		t.Fatal("missing current account")
	}
	if items, err := postgres.NewLocalMediaSearchStore(a.DBPool()).SearchLocalMedia(t.Context(), actor, "%", "multi", 0); err != nil || len(items) != 0 {
		t.Fatal("catalog search treated wildcard as match-all", len(items), err)
	}
	// The independent read pool fails while the real authentication pool remains
	// healthy, so this proves a catalog failure cannot become a 200 [] or 401.
	readPool, err := pgxpool.NewWithConfig(t.Context(), a.DBPool().Config())
	if err != nil {
		t.Fatal("catalog pool initialization failed")
	}
	readPool.Close()
	wrapped := &m5CatalogReadApp{App: a, reader: postgres.NewLocalMediaSearchStore(readPool)}
	failedHandler := httpapi.NewServerHandler(wrapped, authService, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil)
	failedRouter := httpapi.NewRouter(wrapped, failedHandler, authService, nil)
	failedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/search/movies?query=local", nil)
	for _, cookie := range cookies {
		failedRequest.AddCookie(cookie)
	}
	failedResponse := httptest.NewRecorder()
	failedRouter.ServeHTTP(failedResponse, failedRequest)
	if failedResponse.Code != 503 || !strings.Contains(failedResponse.Body.String(), "CATALOG_UNAVAILABLE") {
		t.Fatalf("catalog I/O error was masked: %d %s", failedResponse.Code, failedResponse.Body.String())
	}
	// A rate limit remains an upstream failure, never a 401 or an empty success.
	if response = get("/api/v1/search/movies?query=rate", true); response.Code != 503 {
		t.Fatalf("rate limited: %d", response.Code)
	}
}

func TestM5LocalMetadataCatalogCurrentAccountPG(t *testing.T) {
	a, authService, _ := setupAuthHTTP(t)
	_, token, err := authService.SetupAdmin(t.Context(), "catalog_admin", "Metadata-catalog-fixture-8274!")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := authService.AuthenticateToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	reader := postgres.NewLocalMediaSearchStore(a.DBPool())
	if result, err := reader.SearchLocalMedia(t.Context(), actor, "missing", "multi", 0); err != nil || result == nil || len(result) != 0 {
		t.Fatal("empty catalog", result, err)
	}
	tx, err := a.DBPool().Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), "UPDATE app_user SET is_active=false WHERE id=$1", actor.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		items, e := reader.SearchLocalMedia(t.Context(), actor, "missing", "multi", 0)
		if len(items) != 0 {
			done <- fmt.Errorf("unauthorized items returned")
			return
		}
		done <- e
	}()
	waited := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err = a.DBPool().QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%LockSubscriptionListAccount%')").Scan(&waited); err != nil {
			t.Fatal(err)
		}
		if waited {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waited {
		t.Fatal("reader did not wait for current-account lock")
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("stale account accepted", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not drain")
	}
	// A nonexistent current account is rejected before catalog access.
	disabled := *actor
	disabled.ID = uuid.NewString()
	if _, err = reader.SearchLocalMedia(t.Context(), &disabled, "%", "multi", 0); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("unknown current account accepted", err)
	}
}
