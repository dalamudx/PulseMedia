package app

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

func TestM6DiscoveryFeedNativeHTTPPG(t *testing.T) {
	f := organizationSetup(t)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	password := fmt.Sprintf("Feed-%s-123!", t.Name())
	admin, _, err := authService.SetupAdmin(t.Context(), "feed-admin", password)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusOK)
	var override atomic.Value
	override.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("api_key") != "owned-feed-key" || r.URL.Query().Get("language") != "zh-CN" || r.URL.Query().Get("page") != "2" {
			t.Error("native parameters changed")
		}
		if code := int(status.Load()); code != http.StatusOK {
			w.WriteHeader(code)
		}
		if body := override.Load().(string); body != "" {
			_, _ = io.WriteString(w, body)
			return
		}
		kind := "movie"
		if r.URL.Path == "/tv/popular" || r.URL.Path == "/trending/all/day" {
			kind = "tv"
		}
		switch r.URL.Path {
		case "/movie/popular", "/tv/popular", "/movie/top_rated", "/movie/now_playing", "/trending/all/day":
		default:
			t.Errorf("unexpected list route %s", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"results":[{"id":550,"media_type":%q,"title":"Owned movie","name":"Owned TV","release_date":"2026-01-01","first_air_date":"2026-01-01","vote_average":8,"genre_ids":[18]}]}`, kind)
	}))
	t.Cleanup(upstream.Close)
	provider := tmdb.NewClient("owned-feed-key", tmdb.WithBaseURL(upstream.URL), tmdb.WithHTTPClient(upstream.Client()))
	handler := httpapi.NewServerHandler(a, authService, nil, nil, nil, provider, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(a, handler, authService, nil)
	// Cookie name follows the production auth response, not a test actor header.
	loginBody, err := json.Marshal(map[string]string{"username": "feed-admin", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(loginBody)))
	login.Header.Set("Content-Type", "application/json")
	login.Host = "localhost"
	login.RemoteAddr = "127.0.0.1:1234"
	loginReply := httptest.NewRecorder()
	router.ServeHTTP(loginReply, login)
	if loginReply.Code != http.StatusOK || len(loginReply.Result().Cookies()) == 0 {
		t.Fatalf("real login failed: %d", loginReply.Code)
	}
	cookie := loginReply.Result().Cookies()[0]
	get := func(path string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	before := map[string]int{}
	for _, table := range []string{"job", "download_task", "subscription", "media_unit"} {
		before[table] = f.count(t, table)
	}
	for _, category := range []string{"trending", "movie", "tv", "top_rated", "now_playing"} {
		t.Run(category, func(t *testing.T) {
			response := get("/api/v1/discover?category="+category+"&page=2", true)
			var body struct {
				Results []struct {
					ID     int64    `json:"tmdb_id"`
					Kind   string   `json:"media_type"`
					Genres []string `json:"genres"`
				} `json:"results"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Results) != 1 || body.Results[0].ID != 550 || len(body.Results[0].Genres) != 1 {
				t.Fatalf("native list failed: %d %s", response.Code, response.Body.String())
			}
			want := "movie"
			if category == "tv" || category == "trending" {
				want = "tv"
			}
			if body.Results[0].Kind != want {
				t.Fatal("movie/TV namespace changed")
			}
		})
	}
	for _, query := range []string{"category=unknown", "page=0", "page=-1", "page=501", "page=broken"} {
		t.Run(query, func(t *testing.T) {
			count := calls.Load()
			if response := get("/api/v1/discover?"+query, true); response.Code != http.StatusBadRequest || calls.Load() != count {
				t.Fatalf("invalid query admitted: %d", response.Code)
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		override.Store(`{"results":[]}`)
		response := get("/api/v1/discover?category=now_playing&page=2", true)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"results":[]`) {
			t.Fatalf("valid empty rejected: %d %s", response.Code, response.Body.String())
		}
	})
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"unavailable", `{"secret":"feed-private-value"}`, http.StatusServiceUnavailable},
		{"malformed", `{"secret":"feed-private-value"}`, http.StatusOK},
		{"untyped", `{"results":[{"id":1,"title":"Wrong"}]}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status.Store(int32(tc.status))
			override.Store(tc.body)
			response := get("/api/v1/discover?category=trending&page=2", true)
			if response.Code != http.StatusServiceUnavailable || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/problem+json") || strings.Contains(response.Body.String(), "feed-private-value") || strings.Contains(response.Body.String(), upstream.URL) {
				t.Fatalf("unsafe/false empty failure: %d %s", response.Code, response.Body.String())
			}
		})
	}
	status.Store(http.StatusOK)
	override.Store("")
	t.Run("member", func(t *testing.T) {
		if _, err := f.pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, admin.ID); err != nil {
			t.Fatal(err)
		}
		if response := get("/api/v1/discover?category=movie&page=2", true); response.Code != http.StatusOK {
			t.Fatalf("member browse denied: %d", response.Code)
		}
	})
	t.Run("unauthenticated", func(t *testing.T) {
		count := calls.Load()
		if response := get("/api/v1/discover?category=movie&page=2", false); response.Code != http.StatusUnauthorized || calls.Load() != count {
			t.Fatalf("unauthenticated native call: %d", response.Code)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		if _, err := f.pool.Exec(t.Context(), `UPDATE app_user SET is_active=false WHERE id=$1`, admin.ID); err != nil {
			t.Fatal(err)
		}
		count := calls.Load()
		if response := get("/api/v1/discover?category=movie&page=2", true); response.Code != http.StatusUnauthorized || calls.Load() != count {
			t.Fatalf("disabled native call: %d", response.Code)
		}
	})
	for table, count := range before {
		if f.count(t, table) != count {
			t.Fatalf("browse mutated %s", table)
		}
	}
}
