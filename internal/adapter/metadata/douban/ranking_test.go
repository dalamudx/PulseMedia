package douban_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Mirrors the signature scheme under test.
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/douban"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	testKey    = "test-api-key"
	testSecret = "test-signing-secret"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time                  { return c.now }
func (c fixedClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

func serve(t *testing.T, handler http.HandlerFunc) (*douban.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return douban.NewClient(server.URL, server.Client(), douban.WithCredentials(douban.Credentials{APIKey: testKey, Secret: testSecret})), &calls
}

func subject(id, extra string) string {
	return fmt.Sprintf(`{"id":%q,"title":"片名%s","original_title":"Title","year":"2018","rating":{"value":8.5},"pic":{"large":"https://img3.doubanio.com/a.jpg"}%s}`, id, id, extra)
}

func wrap(items ...string) string {
	return `{"subject_collection_items":[` + strings.Join(items, ",") + `]}`
}

func TestRankingRequestShapeAndValidation(t *testing.T) {
	cases := []struct {
		list, path, kind string
		start            int
	}{
		{ports.DoubanListHotMovie, "/subject_collection/movie_hot_gaia/items", "movie", 0},
		{ports.DoubanListHotTV, "/subject_collection/tv_hot/items", "tv", 20},
		{ports.DoubanListWeeklyChinese, "/subject_collection/tv_chinese_best_weekly/items", "tv", 0},
		{ports.DoubanListWeeklyGlobal, "/subject_collection/tv_global_best_weekly/items", "tv", 0},
		{ports.DoubanListTop250, "/subject_collection/movie_top250/items", "movie", 20},
	}
	for _, tc := range cases {
		t.Run(tc.list, func(t *testing.T) {
			client, calls := serve(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.Method != http.MethodGet || r.URL.Path != tc.path || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" ||
					q.Get("apiKey") != testKey || q.Get("os_rom") != "android" || q.Get("start") != fmt.Sprint(tc.start) || q.Get("count") != "5" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				_, _ = fmt.Fprint(w, wrap(subject("1", ""), subject("2", ""), subject("1", "")))
			})
			items, err := client.GetRanking(t.Context(), tc.list, tc.start, 5)
			if err != nil || len(items) != 2 || items[0].DoubanID != "1" || items[1].DoubanID != "2" || items[0].MediaType != tc.kind || items[0].Year != 2018 || items[0].Rating != 8.5 || items[0].PosterURL != "https://img3.doubanio.com/a.jpg" || calls.Load() != 1 {
				t.Fatalf("%v %+v calls=%d", err, items, calls.Load())
			}
		})
	}
}

func TestRankingSignature(t *testing.T) {
	now := time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC)
	var seen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mac := hmac.New(sha1.New, []byte(testSecret))
		mac.Write([]byte("GET&" + url.QueryEscape("/api/v2/subject_collection/movie_hot_gaia/items") + "&20261020"))
		if q.Get("_ts") != "20261020" || q.Get("_sig") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			t.Errorf("bad signature: ts=%q sig=%q", q.Get("_ts"), q.Get("_sig"))
		}
		seen.Store(true)
		_, _ = fmt.Fprint(w, wrap())
	}))
	defer server.Close()
	client := douban.NewClient(server.URL+"/api/v2", server.Client(),
		douban.WithCredentials(douban.Credentials{APIKey: testKey, Secret: testSecret}), douban.WithClock(fixedClock{now}))
	if items, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); err != nil || len(items) != 0 || !seen.Load() {
		t.Fatalf("%v %+v", err, items)
	}
}

func TestRankingWithoutCredentialsNeverTouchesNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for name, opts := range map[string][]douban.Option{
		"none":         nil,
		"empty key":    {douban.WithCredentials(douban.Credentials{Secret: testSecret})},
		"empty secret": {douban.WithCredentials(douban.Credentials{APIKey: testKey})},
		"source empty": {douban.WithCredentialSource(func(context.Context) douban.Credentials { return douban.Credentials{} })},
	} {
		_, err := douban.NewClient(server.URL, server.Client(), opts...).GetRanking(t.Context(), ports.DoubanListTop250, 0, 5)
		if !errors.Is(err, ports.ErrUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("network used without credentials")
	}
}

func TestRankingCredentialSourceIsReadPerRequest(t *testing.T) {
	var key atomic.Value
	key.Store("first")
	var last atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last.Store(r.URL.Query().Get("apiKey"))
		_, _ = fmt.Fprint(w, wrap())
	}))
	defer server.Close()
	client := douban.NewClient(server.URL, server.Client(), douban.WithCredentialSource(func(context.Context) douban.Credentials {
		return douban.Credentials{APIKey: key.Load().(string), Secret: testSecret}
	}))
	for _, want := range []string{"first", "second"} {
		key.Store(want)
		if _, err := client.GetRanking(t.Context(), ports.DoubanListTop250, 0, 5); err != nil || last.Load() != want {
			t.Fatalf("%s: %v %v", want, err, last.Load())
		}
	}
}

func TestRankingTruncatesToCount(t *testing.T) {
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, wrap(subject("1", ""), subject("2", ""), subject("3", "")))
	})
	items, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 2)
	if err != nil || len(items) != 2 {
		t.Fatalf("%v %+v", err, items)
	}
}

func TestRankingRejectsBadArgumentsBeforeNetwork(t *testing.T) {
	client, calls := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, wrap()) })
	for name, args := range map[string][3]any{
		"unknown":     {"nope", 0, 5},
		"old weekly":  {"weekly_best", 0, 5},
		"negative":    {ports.DoubanListTop250, -1, 5},
		"zero count":  {ports.DoubanListTop250, 0, 0},
		"large count": {ports.DoubanListTop250, 0, 51},
	} {
		if _, err := client.GetRanking(t.Context(), args[0].(string), args[1].(int), args[2].(int)); !errors.Is(err, ports.ErrRejected) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("network used for rejected arguments")
	}
}

func TestRankingRejectsMalformedProviderData(t *testing.T) {
	bad := map[string]string{
		"not json":      `nope`,
		"no list key":   `{}`,
		"old key":       `{"subjects":[]}`,
		"text id":       wrap(subject("12a", "")),
		"empty id":      wrap(subject("", "")),
		"foreign type":  wrap(subject("1", `,"type":"tv"`)),
		"blank title":   wrap(`{"id":"1","title":"  "}`),
		"bad year":      wrap(`{"id":"1","title":"a","year":"20x8"}`),
		"high rating":   wrap(`{"id":"1","title":"a","rating":{"value":10.5}}`),
		"low rating":    wrap(`{"id":"1","title":"a","rating":{"value":-1}}`),
		"long title":    wrap(`{"id":"1","title":"` + strings.Repeat("长", 201) + `"}`),
		"numeric id":    wrap(`{"id":1,"title":"a"}`),
		"too many rows": wrap(append(strings.Split(strings.Repeat(subject("1", "")+"\n", 100), "\n")[:100], subject("2", ""))...),
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) })
			if _, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
}

func TestRankingPosterHostsAreRestricted(t *testing.T) {
	for _, raw := range []string{
		"http://img3.doubanio.com/a.jpg", "https://evil.example/a.jpg", "https://img3.doubanio.com.evil.example/a.jpg",
		"https://u:p@img3.doubanio.com/a.jpg", "https://img3.doubanio.com:8443/a.jpg", "//img3.doubanio.com/a.jpg",
		"https://img3.doubanio.com/view/photo/s_ratio_poster/public/movie_large.jpg",
		"https://img3.doubanio.com/f/shire/tv_normal.png", "https://img3.doubanio.com/tv_large.jpg",
	} {
		client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, wrap(fmt.Sprintf(`{"id":"1","title":"a","pic":{"large":%q}}`, raw)))
		})
		items, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5)
		if err != nil || len(items) != 1 || items[0].PosterURL != "" {
			t.Fatalf("%s: %v %+v", raw, err, items)
		}
	}
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, wrap(`{"id":"1","title":"a","pic":{"large":"https://evil.example/a.jpg","normal":"https://img1.doubanio.com/n.jpg"}}`))
	})
	items, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5)
	if err != nil || items[0].PosterURL != "https://img1.doubanio.com/n.jpg" {
		t.Fatalf("%v %+v", err, items)
	}
}

func TestRankingNeverFollowsRedirects(t *testing.T) {
	var target atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target.Add(1)
		_, _ = fmt.Fprint(w, wrap())
	}))
	defer other.Close()
	client, calls := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	})
	if _, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.Is(err, ports.ErrBadResponse) || target.Load() != 0 || calls.Load() != 1 {
		t.Fatalf("%v target=%d calls=%d", err, target.Load(), calls.Load())
	}
}

func TestRankingBodyLimit(t *testing.T) {
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"subject_collection_items":[],"pad":"`+strings.Repeat("x", 2<<20)+`"}`)
	})
	if _, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatalf("oversized body accepted: %v", err)
	}
}

func TestRankingErrorMappingNeverLeaksSecrets(t *testing.T) {
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	var rate *ports.RateLimitError
	if _, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.As(err, &rate) {
		t.Fatalf("429: %v", err)
	}
	client, _ = serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if _, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatalf("500: %v", err)
	}
	// Unreachable server: the transport error embeds the signed URL, so only a sentinel may escape.
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close()
	_, err := douban.NewClient(address, nil, douban.WithCredentials(douban.Credentials{APIKey: testKey, Secret: testSecret})).GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5)
	if !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "_sig") {
		t.Fatalf("unavailable: %v", err)
	}
}

func TestRankingTimeoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := client.GetRanking(ctx, ports.DoubanListHotMovie, 0, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	slow := &http.Client{Timeout: 50 * time.Millisecond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer server.Close()
	timeout := douban.NewClient(server.URL, slow, douban.WithCredentials(douban.Credentials{APIKey: testKey, Secret: testSecret}))
	if _, err := timeout.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.Is(err, ports.ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestRankingRealCollectionShapes(t *testing.T) {
	// Shapes observed from the live service: hot movies expose cover.url, the
	// weekly and Top250 boards expose pic/cover_url and only a subtitle year.
	body := wrap(
		`{"id":"1","type":"movie","title":"热门","year":"2026","card_subtitle":"2026 / 韩国","cover":{"url":"https://img2.doubanio.com/c.webp","width":1}}`,
		`{"id":"2","type":"movie","title":"经典","card_subtitle":"1994 / 美国 / 剧情","pic":{"large":"https://img3.doubanio.com/l.webp"},"cover_url":"https://img3.doubanio.com/l.webp"}`,
		`{"id":"3","type":"movie","title":"仅封面串","card_subtitle":"2026","cover":"not-an-object"}`,
		`{"id":"4","type":"movie","title":"无年份","card_subtitle":"2026-05-01(中国大陆) / 剧情","cover_url":"https://img3.doubanio.com/u.webp"}`,
		`{"id":"5","type":"movie","title":"伪年份","card_subtitle":"12345 / 美国"}`,
	)
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) })
	items, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5)
	if err != nil || len(items) != 5 {
		t.Fatalf("%v %+v", err, items)
	}
	want := []struct {
		year   int
		poster string
	}{{2026, "https://img2.doubanio.com/c.webp"}, {1994, "https://img3.doubanio.com/l.webp"}, {2026, ""}, {0, "https://img3.doubanio.com/u.webp"}, {0, ""}}
	for i, w := range want {
		if items[i].Year != w.year || items[i].PosterURL != w.poster {
			t.Errorf("item %d: year=%d poster=%q, want %d %q", i+1, items[i].Year, items[i].PosterURL, w.year, w.poster)
		}
	}
}
