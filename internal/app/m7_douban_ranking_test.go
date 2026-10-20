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

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/douban"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/ranking"
)

// Real Cookie/App/PG with the native Douban and TMDB clients pointed at owned
// loopback fixtures. No upstream credentials or public network are involved.
func TestM7DoubanRankingNativeHTTPPG(t *testing.T) {
	f := organizationSetup(t)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	password := fmt.Sprintf("Rank-%s-123!", t.Name())
	admin, _, err := authService.SetupAdmin(t.Context(), "rank-admin", password)
	if err != nil {
		t.Fatal(err)
	}

	var doubanCalls, tmdbCalls atomic.Int32
	var doubanStatus, tmdbStatus atomic.Int32
	doubanStatus.Store(http.StatusOK)
	tmdbStatus.Store(http.StatusOK)
	var doubanBody atomic.Value
	doubanBody.Store("")
	var lastQuery atomic.Value
	lastQuery.Store("")

	row := func(id, title, original, year, extra string) string {
		return fmt.Sprintf(`{"id":%q,"title":%q,"original_title":%q,"year":%q,"rating":{"value":8.9},"pic":{"large":"https://img9.doubanio.com/%s.jpg"}%s}`, id, title, original, year, id, extra)
	}
	doubanServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doubanCalls.Add(1)
		lastQuery.Store(r.URL.RawQuery)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credentials reached Douban")
		}
		if code := int(doubanStatus.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		if body := doubanBody.Load().(string); body != "" {
			_, _ = io.WriteString(w, body)
			return
		}
		switch r.URL.Path {
		case "/subject_collection/movie_hot_gaia/items":
			evil := `{"id":"7","title":"外链海报","year":"2021","rating":{"value":7},"pic":{"large":"https://evil.example/x.jpg"}}`
			_, _ = fmt.Fprintf(w, `{"subject_collection_items":[%s,%s,%s,%s]}`, row("26752088", "我不是药神", "Dying to Survive", "2018", ""), row("5", "歧义片", "", "2020", ""), row("6", "无年份", "", "", ""), evil)
		case "/subject_collection/tv_hot/items":
			_, _ = fmt.Fprintf(w, `{"subject_collection_items":[%s]}`, row("26752089", "绝命毒师", "Breaking Bad", "2008", `,"type":"tv"`))
		case "/subject_collection/tv_chinese_best_weekly/items", "/subject_collection/tv_global_best_weekly/items":
			_, _ = fmt.Fprintf(w, `{"subject_collection_items":[%s]}`, row("30000000", "绝命毒师", "Breaking Bad", "2008", `,"type":"tv"`))
		case "/subject_collection/movie_top250/items":
			var start int
			_, _ = fmt.Sscanf(r.URL.Query().Get("start"), "%d", &start)
			if r.URL.Query().Get("count") != "20" {
				t.Errorf("top250 window %q", r.URL.RawQuery)
			}
			_, _ = fmt.Fprintf(w, `{"subject_collection_items":[%s]}`, row(fmt.Sprint(start+1), "我不是药神", "", "2018", ""))
		default:
			t.Errorf("unexpected Douban route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(doubanServer.Close)

	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tmdbCalls.Add(1)
		if code := int(tmdbStatus.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/search/movie" && q.Get("query") == "我不是药神" && q.Get("year") == "2018":
			_, _ = io.WriteString(w, `{"results":[{"id":100,"title":"我不是药神","original_title":"Dying to Survive","release_date":"2018-07-05"}]}`)
		case r.URL.Path == "/search/movie" && q.Get("query") == "歧义片" && q.Get("year") == "2020":
			_, _ = io.WriteString(w, `{"results":[{"id":201,"title":"歧义片","release_date":"2020-01-01"},{"id":202,"title":"歧义片","release_date":"2020-02-02"}]}`)
		case r.URL.Path == "/search/movie" && q.Get("query") == "外链海报":
			_, _ = io.WriteString(w, `{"results":[]}`)
		case r.URL.Path == "/search/tv" && q.Get("query") == "绝命毒师" && q.Get("first_air_date_year") == "2008":
			_, _ = io.WriteString(w, `{"results":[{"id":1396,"name":"绝命毒师","original_name":"Breaking Bad","first_air_date":"2008-01-20"}]}`)
		default:
			t.Errorf("unexpected TMDB lookup %s?%s", r.URL.Path, r.URL.RawQuery)
			_, _ = io.WriteString(w, `{"results":[]}`)
		}
	}))
	t.Cleanup(tmdbServer.Close)

	provider := tmdb.NewClient("owned-rank-key", tmdb.WithBaseURL(tmdbServer.URL), tmdb.WithHTTPClient(tmdbServer.Client()))
	handler := httpapi.NewServerHandler(a, authService, nil, nil, nil, provider, nil, nil, nil, nil, nil, nil, nil)
	handler.SetDoubanRankingService(ranking.NewService(douban.NewClient(doubanServer.URL, doubanServer.Client(), douban.WithCredentials(douban.Credentials{APIKey: "owned-douban-key", Secret: "owned-douban-secret"})), provider, clock.New()))
	router := httpapi.NewRouter(a, handler, authService, nil)

	loginBody, _ := json.Marshal(map[string]string{"username": "rank-admin", "password": password})
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(loginBody)))
	login.Header.Set("Content-Type", "application/json")
	login.Host, login.RemoteAddr = "localhost", "127.0.0.1:1234"
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
	type result struct {
		DoubanID  string   `json:"douban_id"`
		TMDBID    *int64   `json:"tmdb_id"`
		MediaType string   `json:"media_type"`
		Title     string   `json:"title"`
		Year      *int     `json:"year"`
		Rating    *float64 `json:"rating"`
		Poster    *string  `json:"poster_url"`
	}
	list := func(t *testing.T, path string) []result {
		t.Helper()
		response := get(path, true)
		var body struct {
			Results []result `json:"results"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Results == nil {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		return body.Results
	}
	calls := func() (int32, int32) { return doubanCalls.Load(), tmdbCalls.Load() }
	before := map[string]int{}
	for _, table := range []string{"job", "download_task", "subscription", "media_unit", "media"} {
		before[table] = f.count(t, table)
	}

	t.Run("hot movie maps only unique exact identities", func(t *testing.T) {
		items := list(t, "/api/v1/discover/douban?list=hot_movie")
		if len(items) != 4 || items[0].DoubanID != "26752088" || items[0].TMDBID == nil || *items[0].TMDBID != 100 || items[0].MediaType != "movie" || items[0].Year == nil || *items[0].Year != 2018 || items[0].Rating == nil || items[0].Poster == nil || *items[0].Poster != "https://img9.doubanio.com/26752088.jpg" {
			t.Fatalf("%+v", items)
		}
		if items[1].TMDBID != nil || items[2].TMDBID != nil || items[3].TMDBID != nil {
			t.Fatalf("ambiguous, yearless or unmatched item became subscribable: %+v", items)
		}
		if items[3].Poster != nil {
			t.Fatal("foreign poster host passed through")
		}
		if d, m := calls(); d != 1 || m != 3 { // matched, ambiguous, external-poster; yearless costs nothing
			t.Fatalf("calls douban=%d tmdb=%d", d, m)
		}
	})
	t.Run("repeat read is served from cache", func(t *testing.T) {
		d, m := calls()
		if items := list(t, "/api/v1/discover/douban?list=hot_movie&page=1"); len(items) != 4 {
			t.Fatal(items)
		}
		if d2, m2 := calls(); d2 != d || m2 != m {
			t.Fatalf("cache missed: %d/%d -> %d/%d", d, m, d2, m2)
		}
	})
	t.Run("hot tv weekly best and paged top250", func(t *testing.T) {
		if items := list(t, "/api/v1/discover/douban?list=hot_tv"); len(items) != 1 || items[0].MediaType != "tv" || items[0].TMDBID == nil || *items[0].TMDBID != 1396 {
			t.Fatalf("%+v", items)
		}
		for _, board := range []string{"weekly_chinese", "weekly_global"} {
			if items := list(t, "/api/v1/discover/douban?list="+board); len(items) != 1 || items[0].DoubanID != "30000000" || items[0].MediaType != "tv" || items[0].TMDBID == nil || *items[0].TMDBID != 1396 {
				t.Fatalf("%+v", items)
			}
		}
		for page, wantStart := range map[int]int{1: 0, 2: 20, 13: 240} {
			items := list(t, fmt.Sprintf("/api/v1/discover/douban?list=top250&page=%d", page))
			if len(items) != 1 || items[0].DoubanID != fmt.Sprint(wantStart+1) || !strings.Contains(lastQuery.Load().(string), fmt.Sprintf("start=%d", wantStart)) {
				t.Fatalf("page %d: %+v %v", page, items, lastQuery.Load())
			}
		}
	})
	t.Run("invalid input causes no upstream I/O", func(t *testing.T) {
		d, m := calls()
		for _, query := range []string{"list=unknown", "list=", "page=1", "list=top250&page=0", "list=top250&page=-1", "list=top250&page=14", "list=top250&page=broken", "list=hot_movie&page=6", "list=weekly_chinese&page=2", "list=weekly_global&page=2", "list=weekly_best"} {
			if response := get("/api/v1/discover/douban?"+query, true); response.Code != http.StatusBadRequest {
				t.Fatalf("%s admitted: %d %s", query, response.Code, response.Body.String())
			}
		}
		if d2, m2 := calls(); d2 != d || m2 != m {
			t.Fatal("rejected input reached an upstream")
		}
	})
	t.Run("provider failures are 503 and never cached", func(t *testing.T) {
		const path = "/api/v1/discover/douban?list=top250&page=3"
		for name, set := range map[string]func(){
			"status":    func() { doubanStatus.Store(http.StatusInternalServerError) },
			"malformed": func() { doubanStatus.Store(http.StatusOK); doubanBody.Store(`{"secret":"rank-private-value"}`) },
			"rate":      func() { doubanBody.Store(""); doubanStatus.Store(http.StatusTooManyRequests) },
		} {
			set()
			response := get(path, true)
			if response.Code != http.StatusServiceUnavailable || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/problem+json") || strings.Contains(response.Body.String(), "rank-private-value") || strings.Contains(response.Body.String(), doubanServer.URL) {
				t.Fatalf("%s: unsafe or false-empty failure: %d %s", name, response.Code, response.Body.String())
			}
		}
		doubanStatus.Store(http.StatusOK)
		doubanBody.Store("")
		if items := list(t, path); len(items) != 1 || items[0].DoubanID != "41" {
			t.Fatalf("recovery after failure: %+v", items)
		}
	})
	t.Run("TMDB lookup failure degrades to unmapped and is not cached", func(t *testing.T) {
		const path = "/api/v1/discover/douban?list=top250&page=4"
		tmdbStatus.Store(http.StatusInternalServerError)
		items := list(t, path)
		if len(items) != 1 || items[0].TMDBID != nil || items[0].Title == "" {
			t.Fatalf("%+v", items)
		}
		tmdbStatus.Store(http.StatusOK)
		d, _ := calls()
		if items = list(t, path); items[0].TMDBID == nil || *items[0].TMDBID != 100 {
			t.Fatalf("degraded result was cached: %+v", items)
		}
		if d2, _ := calls(); d2 != d+1 {
			t.Fatal("recovery did not re-read the provider")
		}
	})
	t.Run("member browses, anonymous and disabled accounts do not", func(t *testing.T) {
		if _, err := f.pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, admin.ID); err != nil {
			t.Fatal(err)
		}
		if response := get("/api/v1/discover/douban?list=hot_tv", true); response.Code != http.StatusOK {
			t.Fatalf("member denied: %d", response.Code)
		}
		d, m := calls()
		if response := get("/api/v1/discover/douban?list=top250&page=5", false); response.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d", response.Code)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE app_user SET is_active=false WHERE id=$1`, admin.ID); err != nil {
			t.Fatal(err)
		}
		if response := get("/api/v1/discover/douban?list=top250&page=5", true); response.Code != http.StatusUnauthorized {
			t.Fatalf("disabled: %d", response.Code)
		}
		if d2, m2 := calls(); d2 != d || m2 != m {
			t.Fatal("unauthorized request reached an upstream")
		}
	})
	for table, count := range before {
		if f.count(t, table) != count {
			t.Fatalf("ranking read mutated %s", table)
		}
	}
}
