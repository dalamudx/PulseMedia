package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadata"
	"github.com/google/uuid"
)

const m6DetailBase = "/api/v1/media/tmdb/"
const m6DetailKey = "owned-m6-detail-key"
const m6DetailSecret = "owned-upstream-secret"

type m6DetailReply struct {
	status int
	body   string
}

type m6DetailAcceptance struct {
	f       *organizationFixture
	handler *httpapi.ServerHandler
	router  http.Handler
	cookie  *http.Cookie
	userID  uuid.UUID
	read    string
	write   string
	server  *httptest.Server
	logs    bytes.Buffer
	mu      sync.Mutex
	replies map[string]m6DetailReply
	calls   []string
}

// This fixture starts neither App.Run nor workers. All metadata comes through
// the native TMDB decoder; only identity/session/token setup writes to the DB.
func newM6DetailAcceptance(t *testing.T) *m6DetailAcceptance {
	t.Helper()
	if !strings.HasPrefix(filepath.Clean(os.TempDir()), "/data/temp/") {
		t.Fatal("run with task-owned TMPDIR under /data/temp")
	}
	f := organizationSetup(t)
	var version int
	if err := f.pool.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version != 180006 {
		t.Fatal("real PostgreSQL 18.6 required", err)
	}
	h := &m6DetailAcceptance{f: f, replies: make(map[string]m6DetailReply), read: "owned-m6-detail-read-token", write: "owned-m6-detail-write-token"}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(&h.logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	service := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	password := "Owned-M6-Detail-123!"
	user, _, err := service.SetupAdmin(t.Context(), "detail-admin", password)
	if err != nil {
		t.Fatal(err)
	}
	h.userID = user.ID
	for _, token := range []struct{ raw, scopes string }{{h.read, `["read"]`}, {h.write, `["write"]`}} {
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'owned detail acceptance',$3,$4::jsonb)`, uuid.New(), user.ID, crypto.HashToken(token.raw), token.scopes); err != nil {
			t.Fatal(err)
		}
	}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.calls = append(h.calls, r.URL.Path)
		reply, ok := h.replies[r.URL.Path]
		h.mu.Unlock()
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" || q.Get("language") != "zh-CN" || len(q["language"]) != 1 || q.Get("api_key") != m6DetailKey || len(q["api_key"]) != 1 || len(q) != 2 || r.Header.Get("Authorization") != "" {
			t.Error("native request changed method, default language, key or query namespace")
		}
		if !ok {
			t.Errorf("unexpected native endpoint: %s", r.URL.Path)
			reply = m6DetailReply{http.StatusInternalServerError, `{}`}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Location", fmt.Sprintf("/native/3/trap?private=%s", m6DetailSecret))
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(h.server.Close)
	h.seed(t)
	h.handler = httpapi.NewServerHandler(a, service, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.native()
	h.router = httpapi.NewRouter(a, h.handler, service, nil)
	body, err := json.Marshal(map[string]string{"username": "detail-admin", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body)).WithContext(t.Context())
	request.Header.Set("Content-Type", "application/json")
	request.Host, request.RemoteAddr = "localhost", "127.0.0.1:1234"
	response := httptest.NewRecorder()
	h.router.ServeHTTP(response, request)
	result := response.Result()
	defer func() { _ = result.Body.Close() }()
	for _, cookie := range result.Cookies() {
		if cookie.Name == "pulsemedia_session" {
			h.cookie = cookie
		}
	}
	if response.Code != http.StatusOK || h.cookie == nil || h.cookie.Value == "" || !h.cookie.HttpOnly || h.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("real auth.Service Login did not issue the public session cookie", response.Code)
	}
	before, err := m6DashboardSnapshot(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		after, err := m6DashboardSnapshot(ctx, f)
		if err != nil {
			t.Error("business snapshot failed", err)
		}
		for table, digest := range before {
			if after[table] != digest {
				t.Errorf("metadata browse mutated business table %s", table)
			}
		}
		if len(after) != len(before) {
			t.Error("metadata browse changed business schema")
		}
		h.redacted(t, h.logs.String())
	})
	return h
}

func (h *m6DetailAcceptance) seed(t *testing.T) {
	t.Helper()
	h.set("movie/42", 200, `{"id":42,"title":"同号电影","original_title":"Owned movie","overview":"电影剧情，不触发英文回退","release_date":"2024-02-29","poster_path":"/movie.jpg","backdrop_path":"/movie-back.jpg","runtime":123,"vote_average":8.5,"genres":[{"name":"剧情"}]}`)
	h.set("tv/42", 200, `{"id":42,"name":"同号剧集","original_name":"Owned TV","overview":"剧集剧情","first_air_date":"2023-01-02","poster_path":"/tv.jpg","backdrop_path":"/tv-back.jpg","vote_average":9,"genres":[{"name":"悬疑"}],"number_of_seasons":2,"number_of_episodes":4,"seasons":[{"season_number":0,"name":"特别篇","episode_count":0},{"season_number":1,"name":"第一季","episode_count":2,"air_date":"2024-02-29","poster_path":"/season.jpg"},{"season_number":3,"name":"第三季","episode_count":2}]}`)
	cast := make([]map[string]any, 27)
	for i := range cast {
		cast[i] = map[string]any{"id": 100 - i, "name": fmt.Sprintf("演员%d", i), "character": fmt.Sprintf("角色%d", i), "order": 27 - i, "profile_path": "/actor.jpg"}
	}
	body, err := json.Marshal(map[string]any{"id": 42, "cast": cast})
	if err != nil {
		t.Fatal(err)
	}
	h.set("movie/42/credits", 200, string(body))
	h.set("tv/42/credits", 200, `{"id":42,"cast":[{"id":201,"name":"剧集演员","profile_path":null}]}`)
	h.set("tv/42/season/0", 200, `{"season_number":0,"episodes":[]}`)
	h.set("tv/42/season/1", 200, `{"season_number":1,"episodes":[{"id":401,"episode_number":1,"season_number":1,"show_id":42,"name":"第一集","overview":"真实剧情","air_date":"2024-02-29","still_path":"/still.jpg"},{"id":403,"episode_number":3,"season_number":1,"show_id":42,"air_date":"","still_path":null}]}`)
	h.set("tv/42/season/3", 200, `{"season_number":3,"episodes":[{"id":503,"episode_number":3,"show_id":42},{"id":501,"episode_number":1,"show_id":42}]}`)
}

func (h *m6DetailAcceptance) set(path string, status int, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replies["/native/3/"+path] = m6DetailReply{status, body}
}

func (h *m6DetailAcceptance) native() {
	client := h.server.Client()
	client.Timeout = 2 * time.Second
	provider := tmdb.NewClient(m6DetailKey, tmdb.WithBaseURL(h.server.URL+"/native/3"), tmdb.WithHTTPClient(client))
	h.handler.SetMetadataProvider(provider)
	h.handler.SetMetadataDetailService(metadata.NewDetailService(provider))
}

func (h *m6DetailAcceptance) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

func (h *m6DetailAcceptance) get(t *testing.T, path, credential string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, m6DetailBase+path, nil).WithContext(ctx)
	request.Header.Set("X-Request-ID", "m6-details-acceptance")
	switch credential {
	case "cookie":
		request.AddCookie(h.cookie)
	case "":
	default:
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response := httptest.NewRecorder()
	h.router.ServeHTTP(response, request)
	h.redacted(t, response.Body.String())
	return response
}

func (h *m6DetailAcceptance) redacted(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{m6DetailKey, m6DetailSecret, h.server.URL, h.cookie.Value, h.read, h.write, "api_key=", "postgres://"} {
		if strings.Contains(text, secret) {
			t.Fatal("metadata response/log exposed private source or authentication data")
		}
	}
}

func m6DetailJSON[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected JSON 200, got %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func m6DetailProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code, detail string) {
	t.Helper()
	var problem httpapi.ErrorResponse
	if response.Code != status || response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &problem) != nil {
		t.Fatalf("expected RFC9457 %d, got %d: %s", status, response.Code, response.Body.String())
	}
	if problem.Type != "about:blank" || problem.Title != http.StatusText(status) || problem.Status != status || problem.Code == "" || (code != "" && problem.Code != code) || problem.Detail == "" || problem.Message != problem.Detail || problem.RequestId != "m6-details-acceptance" || problem.RequestId != response.Header().Get("X-Request-ID") || problem.Details != nil {
		t.Fatalf("incomplete RFC9457 problem: %+v", problem)
	}
	if detail != "" && problem.Detail != detail {
		t.Fatalf("wrong public detail: %q", problem.Detail)
	}
}

func TestM6MetadataDetailDisplayNativeHTTPPG(t *testing.T) {
	h := newM6DetailAcceptance(t)
	t.Run("movie_tv_same_id_display", func(t *testing.T) {
		movie := m6DetailJSON[httpapi.TMDBMovieDetailsResponse](t, h.get(t, "movie/42", "cookie"))
		tv := m6DetailJSON[httpapi.TMDBTVDetailsResponse](t, h.get(t, "tv/42", "cookie"))
		if movie.TmdbId != 42 || tv.TmdbId != 42 || movie.Title != "同号电影" || tv.Title != "同号剧集" || movie.Overview == nil || *movie.Overview != "电影剧情，不触发英文回退" || tv.Overview == nil || *tv.Overview != "剧集剧情" || movie.Runtime == nil || *movie.Runtime != 123 || movie.Year == nil || *movie.Year != 2024 || movie.Rating == nil || *movie.Rating != 8.5 {
			t.Fatal("same numeric identity mixed movie/TV display facts", movie, tv)
		}
		if movie.BackdropUrl == nil || *movie.BackdropUrl != "https://image.tmdb.org/t/p/w500/movie-back.jpg" || movie.PosterUrl == nil || *movie.PosterUrl != "https://image.tmdb.org/t/p/w500/movie.jpg" || tv.BackdropUrl == nil || *tv.BackdropUrl != "https://image.tmdb.org/t/p/w500/tv-back.jpg" || tv.PosterUrl == nil || *tv.PosterUrl != "https://image.tmdb.org/t/p/w500/tv.jpg" {
			t.Fatal("lost typed detail image projection")
		}
		if tv.SeasonsCount != 2 || tv.EpisodesCount != 4 || len(tv.Seasons) != 3 || tv.Seasons[0].SeasonNumber != 0 || tv.Seasons[1].SeasonNumber != 1 || tv.Seasons[2].SeasonNumber != 3 || tv.Seasons[2].AirDate != nil {
			t.Fatal("sparse season numbers or unknown date were synthesized", tv.Seasons)
		}
		if h.count() != 2 {
			t.Fatal("unexpected extra native detail call or English fallback")
		}
	})
	t.Run("movie_cast_top24_provider_order_images", func(t *testing.T) {
		credits := m6DetailJSON[httpapi.TMDBCreditsResponse](t, h.get(t, "movie/42/credits", "cookie"))
		if credits.TmdbId != 42 || credits.MediaType != "movie" || len(credits.Cast) != 24 {
			t.Fatal("cast identity/cap changed", credits)
		}
		for i, actor := range credits.Cast {
			if actor.TmdbId != int64(100-i) || actor.Name != fmt.Sprintf("演员%d", i) || actor.Character == nil || *actor.Character != fmt.Sprintf("角色%d", i) || actor.ProfileUrl == nil || *actor.ProfileUrl != "https://image.tmdb.org/t/p/w500/actor.jpg" {
				t.Fatal("cast reordered or image/character evidence lost", i, actor)
			}
		}
	})
	t.Run("tv_cast_exact_namespace_unknown_image", func(t *testing.T) {
		credits := m6DetailJSON[httpapi.TMDBCreditsResponse](t, h.get(t, "tv/42/credits", "cookie"))
		if credits.TmdbId != 42 || credits.MediaType != "tv" || len(credits.Cast) != 1 || credits.Cast[0].TmdbId != 201 || credits.Cast[0].Name != "剧集演员" || credits.Cast[0].ProfileUrl != nil || credits.Cast[0].Character != nil {
			t.Fatal("TV credits reused movie evidence or invented image/character", credits)
		}
	})
	t.Run("valid_empty_credits", func(t *testing.T) {
		h.set("tv/42/credits", 200, `{"id":42,"cast":[]}`)
		credits := m6DetailJSON[httpapi.TMDBCreditsResponse](t, h.get(t, "tv/42/credits", "cookie"))
		if credits.Cast == nil || len(credits.Cast) != 0 || credits.TmdbId != 42 || credits.MediaType != "tv" {
			t.Fatal("real empty cast changed to missing data", credits)
		}
	})
	t.Run("sparse_episodes_1_3_date_unknown", func(t *testing.T) {
		season := m6DetailJSON[httpapi.TMDBSeasonDetailsResponse](t, h.get(t, "tv/42/seasons/1", "cookie"))
		if season.TmdbId != 42 || season.SeasonNumber != 1 || !season.Complete || len(season.Episodes) != 2 || season.Episodes[0].TmdbId != 401 || season.Episodes[0].EpisodeNumber != 1 || season.Episodes[1].TmdbId != 403 || season.Episodes[1].EpisodeNumber != 3 {
			t.Fatal("sparse episode identity/order changed or episode 2 invented", season)
		}
		first, unknown := season.Episodes[0], season.Episodes[1]
		if first.Name == nil || *first.Name != "第一集" || first.Overview == nil || *first.Overview != "真实剧情" || first.AirDate == nil || *first.AirDate != "2024-02-29" || first.StillUrl == nil || *first.StillUrl != "https://image.tmdb.org/t/p/w500/still.jpg" || unknown.Name != nil || unknown.Overview != nil || unknown.AirDate != nil || unknown.StillUrl != nil {
			t.Fatal("episode display evidence/unknowns changed", season.Episodes)
		}
	})
	t.Run("sparse_reverse_provider_order", func(t *testing.T) {
		season := m6DetailJSON[httpapi.TMDBSeasonDetailsResponse](t, h.get(t, "tv/42/seasons/3", "cookie"))
		if !season.Complete || season.SeasonNumber != 3 || len(season.Episodes) != 2 || season.Episodes[0].EpisodeNumber != 3 || season.Episodes[1].EpisodeNumber != 1 {
			t.Fatal("provider order or sparse numbers lost", season)
		}
	})
	t.Run("valid_empty_special_season", func(t *testing.T) {
		season := m6DetailJSON[httpapi.TMDBSeasonDetailsResponse](t, h.get(t, "tv/42/seasons/0", "cookie"))
		if season.TmdbId != 42 || season.SeasonNumber != 0 || !season.Complete || season.Episodes == nil || len(season.Episodes) != 0 {
			t.Fatal("valid empty season became failed/incomplete", season)
		}
	})
	t.Run("unsafe_images_remain_unknown", func(t *testing.T) {
		h.set("tv/42/credits", 200, `{"id":42,"cast":[{"id":201,"name":"演员","profile_path":"https://evil.invalid/private.jpg"}]}`)
		h.set("tv/42/season/0", 200, `{"season_number":0,"episodes":[{"id":601,"episode_number":1,"still_path":"/../private.jpg"}]}`)
		credits := m6DetailJSON[httpapi.TMDBCreditsResponse](t, h.get(t, "tv/42/credits", "cookie"))
		season := m6DetailJSON[httpapi.TMDBSeasonDetailsResponse](t, h.get(t, "tv/42/seasons/0", "cookie"))
		if len(credits.Cast) != 1 || credits.Cast[0].ProfileUrl != nil || len(season.Episodes) != 1 || season.Episodes[0].StillUrl != nil {
			t.Fatal("unsafe source image escaped the native projection")
		}
	})
}

func TestM6MetadataDetailFaultsNativeHTTPPG(t *testing.T) {
	h := newM6DetailAcceptance(t)
	for _, route := range []struct{ name, public, native string }{
		{"movie", "movie/42", "movie/42"}, {"tv", "tv/42", "tv/42"},
		{"movie_credits", "movie/42/credits", "movie/42/credits"}, {"tv_credits", "tv/42/credits", "tv/42/credits"},
		{"season", "tv/42/seasons/1", "tv/42/season/1"},
	} {
		for _, status := range []int{404, 401, 403, 429, 500, 503, 302} {
			t.Run(fmt.Sprintf("%s_source_%d", route.name, status), func(t *testing.T) {
				h.set(route.native, status, fmt.Sprintf(`{"private":%q}`, m6DetailSecret))
				// Fresh real clients isolate movie cache and per-client rate cooldown.
				h.native()
				before := h.count()
				response := h.get(t, route.public, "cookie")
				want, code, detail := 503, "METADATA_UNAVAILABLE", "metadata is temporarily unavailable"
				switch route.name {
				case "movie":
					detail = "movie metadata is temporarily unavailable"
				case "tv":
					detail = "TV metadata is temporarily unavailable"
				}
				if status == 404 {
					want, code, detail = 404, "NOT_FOUND", "metadata not found"
					switch route.name {
					case "movie":
						detail = "movie not found"
					case "tv":
						detail = "TV show not found"
					}
				}
				m6DetailProblem(t, response, want, code, detail)
				if h.count() != before+1 {
					t.Fatal("source failure skipped native call or followed redirect/retried")
				}
			})
		}
	}
	for _, tc := range []struct{ name, public, native, body string }{
		{"movie_identity_mismatch", "movie/42", "movie/42", `{"id":43,"title":"Wrong","overview":"Present"}`},
		{"tv_identity_mismatch", "tv/42", "tv/42", `{"id":43,"name":"Wrong","seasons":[]}`},
		{"tv_namespace_mismatch", "tv/42", "tv/42", `{"id":42,"media_type":"movie","name":"Wrong","seasons":[]}`},
		{"movie_malformed_json", "movie/42", "movie/42", `{"id":`},
		{"tv_malformed_json", "tv/42", "tv/42", `{"id":`},
		{"credits_missing_cast", "movie/42/credits", "movie/42/credits", `{"id":42}`},
		{"credits_null_cast", "tv/42/credits", "tv/42/credits", `{"id":42,"cast":null}`},
		{"credits_identity_mismatch", "movie/42/credits", "movie/42/credits", `{"id":43,"cast":[]}`},
		{"credits_malformed_json", "tv/42/credits", "tv/42/credits", `{"id":`},
		{"credits_invalid_member", "movie/42/credits", "movie/42/credits", `{"id":42,"cast":[{"id":0,"name":"Invalid"}]}`},
		{"season_missing_episodes", "tv/42/seasons/1", "tv/42/season/1", `{"season_number":1}`},
		{"season_null_episodes", "tv/42/seasons/1", "tv/42/season/1", `{"season_number":1,"episodes":null}`},
		{"season_identity_mismatch", "tv/42/seasons/1", "tv/42/season/1", `{"season_number":3,"episodes":[]}`},
		{"episode_show_identity_mismatch", "tv/42/seasons/1", "tv/42/season/1", `{"season_number":1,"episodes":[{"id":401,"episode_number":1,"show_id":43}]}`},
		{"episode_season_identity_mismatch", "tv/42/seasons/1", "tv/42/season/1", `{"season_number":1,"episodes":[{"id":401,"episode_number":1,"season_number":3}]}`},
		{"episode_duplicate_number", "tv/42/seasons/1", "tv/42/season/1", `{"season_number":1,"episodes":[{"id":401,"episode_number":1},{"id":403,"episode_number":1}]}`},
		{"episode_invalid_date", "tv/42/seasons/1", "tv/42/season/1", `{"season_number":1,"episodes":[{"id":401,"episode_number":1,"air_date":"2026-02-29"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.set(tc.native, 200, tc.body)
			h.native()
			before := h.count()
			m6DetailProblem(t, h.get(t, tc.public, "cookie"), 503, "METADATA_UNAVAILABLE", "")
			if h.count() != before+1 {
				t.Fatal("malformed source was not observed exactly once")
			}
		})
	}
	t.Run("credits_invalid_tail_beyond_top24", func(t *testing.T) {
		cast := make([]map[string]any, 25)
		for i := range cast {
			cast[i] = map[string]any{"id": i + 1, "name": "Actor"}
		}
		cast[24]["id"] = 0
		body, err := json.Marshal(map[string]any{"id": 42, "cast": cast})
		if err != nil {
			t.Fatal(err)
		}
		h.set("movie/42/credits", 200, string(body))
		h.native()
		m6DetailProblem(t, h.get(t, "movie/42/credits", "cookie"), 503, "METADATA_UNAVAILABLE", "metadata is temporarily unavailable")
	})
}

func TestM6MetadataDetailAuthorizationNativeHTTPPG(t *testing.T) {
	h := newM6DetailAcceptance(t)
	routes := []string{"movie/42", "tv/42", "movie/42/credits", "tv/42/credits", "tv/42/seasons/1"}
	for i, path := range routes {
		t.Run(fmt.Sprintf("anonymous_%d", i), func(t *testing.T) {
			before := h.count()
			m6DetailProblem(t, h.get(t, path, ""), 401, "UNAUTHORIZED", "authentication required")
			if h.count() != before {
				t.Fatal("anonymous request performed source I/O")
			}
		})
	}
	for i, path := range []string{"movie/0", "tv/-1", "movie/not-an-id", "movie/9223372036854775808", "movie/0/credits", "tv/-1/credits", "person/42/credits", "Movie/42/credits", "tv/42/seasons/-1", "tv/42/seasons/not-a-season", "tv/0/seasons/1", "tv/42/seasons/9223372036854775808"} {
		t.Run(fmt.Sprintf("invalid_input_%02d", i), func(t *testing.T) {
			before := h.count()
			response := h.get(t, path, "cookie")
			m6DetailProblem(t, response, 400, "", "")
			if h.count() != before {
				t.Fatal("invalid request performed source I/O")
			}
		})
	}
	// Fixture transitions intentionally change only the current account. The same
	// cookie is reauthenticated on every request; there is no post-network DB gate.
	if _, err := h.f.pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, h.userID); err != nil {
		t.Fatal(err)
	}
	for _, credential := range []struct{ name, value string }{{"member_cookie", "cookie"}, {"member_read_token", h.read}, {"write_only_token", h.write}} {
		for i, path := range routes {
			t.Run(fmt.Sprintf("%s_%d", credential.name, i), func(t *testing.T) {
				h.native()
				before := h.count()
				response := h.get(t, path, credential.value)
				if credential.name == "write_only_token" {
					m6DetailProblem(t, response, 403, "FORBIDDEN", "API token does not authorize this action")
					if h.count() != before {
						t.Fatal("write-only token performed source I/O")
					}
				} else if response.Code != 200 || h.count() != before+1 {
					t.Fatalf("real member read denied or skipped source: %d %s", response.Code, response.Body.String())
				}
			})
		}
	}
	if _, err := h.f.pool.Exec(t.Context(), `UPDATE app_user SET is_active=false WHERE id=$1`, h.userID); err != nil {
		t.Fatal(err)
	}
	for _, credential := range []struct{ name, value string }{{"cookie", "cookie"}, {"read_token", h.read}} {
		for i, path := range routes {
			t.Run(fmt.Sprintf("disabled_%s_%d", credential.name, i), func(t *testing.T) {
				before := h.count()
				m6DetailProblem(t, h.get(t, path, credential.value), 401, "UNAUTHORIZED", "authentication required")
				if h.count() != before {
					t.Fatal("disabled account performed source I/O")
				}
			})
		}
	}
}

func TestM6MetadataDetailUnavailableNativeHTTPPG(t *testing.T) {
	h := newM6DetailAcceptance(t)
	for _, state := range []string{"nil_provider", "nil_detail_service", "missing_detail_client"} {
		h.handler.SetMetadataProvider(nil)
		if state == "nil_detail_service" {
			h.handler.SetMetadataDetailService(nil)
		} else {
			h.handler.SetMetadataDetailService(metadata.NewDetailService(nil))
		}
		for i, path := range []string{"movie/42", "tv/42", "movie/42/credits", "tv/42/credits", "tv/42/seasons/1"} {
			if state == "missing_detail_client" && i < 2 {
				continue
			}
			t.Run(fmt.Sprintf("%s_%d", state, i), func(t *testing.T) {
				before := h.count()
				m6DetailProblem(t, h.get(t, path, "cookie"), 503, "METADATA_UNAVAILABLE", "")
				if h.count() != before {
					t.Fatal("missing dependency made a network request")
				}
			})
		}
	}
}
