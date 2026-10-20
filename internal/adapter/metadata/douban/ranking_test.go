package douban_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/douban"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func serve(t *testing.T, handler http.HandlerFunc) (*douban.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return douban.NewClient(server.URL, server.Client()), &calls
}

func subject(id, extra string) string {
	return fmt.Sprintf(`{"id":%q,"title":"片名%s","original_title":"Title","year":"2018","rating":{"value":8.5},"pic":{"large":"https://img3.doubanio.com/a.jpg"}%s}`, id, id, extra)
}

func TestRankingRequestShapeAndValidation(t *testing.T) {
	cases := []struct {
		list, path, kind string
		start            int
		query            []string
		body             string
	}{
		{ports.DoubanListHotMovie, "/movie/hot_gaia", "movie", 0, []string{"sort=recommend"}, "subjects"},
		{ports.DoubanListHotTV, "/tv/hot_gaia", "tv", 0, []string{"sort=recommend"}, "subjects"},
		{ports.DoubanListWeeklyBest, "/subject_collection/movie_weekly_best/items", "movie", 0, []string{"start=0", "count=5"}, "subject_collection_items"},
		{ports.DoubanListTop250, "/movie/top250", "movie", 20, []string{"start=20", "count=5"}, "subjects"},
	}
	for _, tc := range cases {
		t.Run(tc.list, func(t *testing.T) {
			client, calls := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != tc.path || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				for _, part := range tc.query {
					if !strings.Contains(r.URL.RawQuery, part) {
						t.Errorf("query %q lacks %s", r.URL.RawQuery, part)
					}
				}
				_, _ = fmt.Fprintf(w, `{%q:[%s,%s,%s]}`, tc.body, subject("1", ""), subject("2", ""), subject("1", ""))
			})
			items, err := client.GetRanking(t.Context(), tc.list, tc.start, 5)
			if err != nil || len(items) != 2 || items[0].DoubanID != "1" || items[1].DoubanID != "2" || items[0].MediaType != tc.kind || items[0].Year != 2018 || items[0].Rating != 8.5 || items[0].PosterURL != "https://img3.doubanio.com/a.jpg" || calls.Load() != 1 {
				t.Fatalf("%v %+v calls=%d", err, items, calls.Load())
			}
		})
	}
}

func TestRankingTruncatesToCountAndCollections(t *testing.T) {
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"selected_collections":[{"items":[%s,%s,%s]}]}`, subject("1", ""), subject("2", ""), subject("3", ""))
	})
	items, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 2)
	if err != nil || len(items) != 2 {
		t.Fatalf("%v %+v", err, items)
	}
}

func TestRankingRejectsBadArgumentsBeforeNetwork(t *testing.T) {
	client, calls := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, `{"subjects":[]}`) })
	for name, args := range map[string][3]any{
		"unknown":      {"nope", 0, 5},
		"negative":     {ports.DoubanListTop250, -1, 5},
		"zero count":   {ports.DoubanListTop250, 0, 0},
		"large count":  {ports.DoubanListTop250, 0, 51},
		"hot paging":   {ports.DoubanListHotMovie, 10, 5},
		"weekly start": {ports.DoubanListWeeklyBest, -5, 5},
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
		"text id":       `{"subjects":[` + subject("12a", "") + `]}`,
		"empty id":      `{"subjects":[` + subject("", "") + `]}`,
		"foreign type":  `{"subjects":[` + subject("1", `,"type":"tv"`) + `]}`,
		"blank title":   `{"subjects":[{"id":"1","title":"  "}]}`,
		"bad year":      `{"subjects":[{"id":"1","title":"a","year":"20x8"}]}`,
		"high rating":   `{"subjects":[{"id":"1","title":"a","rating":{"value":10.5}}]}`,
		"low rating":    `{"subjects":[{"id":"1","title":"a","rating":{"value":-1}}]}`,
		"long title":    `{"subjects":[{"id":"1","title":"` + strings.Repeat("长", 201) + `"}]}`,
		"numeric id":    `{"subjects":[{"id":1,"title":"a"}]}`,
		"too many rows": `{"subjects":[` + strings.Repeat(subject("1", "")+",", 100) + subject("2", "") + `]}`,
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
	for _, raw := range []string{"http://img3.doubanio.com/a.jpg", "https://evil.example/a.jpg", "https://img3.doubanio.com.evil.example/a.jpg", "https://u:p@img3.doubanio.com/a.jpg", "https://img3.doubanio.com:8443/a.jpg", "//img3.doubanio.com/a.jpg"} {
		client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `{"subjects":[{"id":"1","title":"a","pic":{"large":%q}}]}`, raw)
		})
		items, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5)
		if err != nil || len(items) != 1 || items[0].PosterURL != "" {
			t.Fatalf("%s: %v %+v", raw, err, items)
		}
	}
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"subjects":[{"id":"1","title":"a","pic":{"large":"https://evil.example/a.jpg","normal":"https://img1.doubanio.com/n.jpg"}}]}`)
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
		_, _ = fmt.Fprint(w, `{"subjects":[]}`)
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
		_, _ = fmt.Fprint(w, `{"subjects":[],"pad":"`+strings.Repeat("x", 2<<20)+`"}`)
	})
	if _, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatalf("oversized body accepted: %v", err)
	}
}

func TestRankingErrorMapping(t *testing.T) {
	client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	var rate *ports.RateLimitError
	if _, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.As(err, &rate) {
		t.Fatalf("429: %v", err)
	}
	client, _ = serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if _, err := client.GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatalf("500: %v", err)
	}
	// Unreachable server: sentinel only, no address leakage.
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	_, err := douban.NewClient(url, nil).GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5)
	if !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "127.0.0.1") {
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
	if _, err := douban.NewClient(server.URL, slow).GetRanking(t.Context(), ports.DoubanListHotMovie, 0, 5); !errors.Is(err, ports.ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
}
