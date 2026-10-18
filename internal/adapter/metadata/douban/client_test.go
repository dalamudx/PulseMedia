package douban_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/douban"
)

func TestDoubanClientLists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// 1. Hot Movies
		if r.URL.Path == "/movie/hot_gaia" {
			_, _ = fmt.Fprint(w, `{
				"subjects": [
					{
						"id": "26752088",
						"title": "我不是药神",
						"original_title": "Dying to Survive",
						"year": "2018",
						"rating": {"value": 9.0},
						"pic": {"normal": "https://img.doubanio.com/p2561305376.jpg"}
					}
				]
			}`)
			return
		}

		// 2. Hot TV
		if r.URL.Path == "/tv/hot_gaia" {
			_, _ = fmt.Fprint(w, `{
				"subjects": [
					{
						"id": "26752089",
						"title": "绝命毒师",
						"original_title": "Breaking Bad",
						"year": "2008",
						"rating": {"value": 9.7},
						"pic": {"normal": "https://img.doubanio.com/p2561305377.jpg"}
					}
				]
			}`)
			return
		}

		// 3. Top250
		if r.URL.Path == "/movie/top250" {
			_, _ = fmt.Fprint(w, `{
				"subjects": [
					{
						"id": "1292052",
						"title": "肖申克的救赎",
						"original_title": "The Shawshank Redemption",
						"year": "1994",
						"rating": {"value": 9.7},
						"pic": {"normal": "https://img.doubanio.com/p4807472.jpg"}
					}
				]
			}`)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	client := douban.NewClient(server.URL, server.Client())
	ctx := context.Background()

	// 1. Hot Movies
	movies, err := client.GetHotMovies(ctx)
	if err != nil || len(movies) != 1 || movies[0].Title != "我不是药神" || movies[0].Rating != 9.0 {
		t.Fatalf("get hot movies failed: %v, %+v", err, movies)
	}

	// 2. Hot TV
	tv, err := client.GetHotTVShows(ctx)
	if err != nil || len(tv) != 1 || tv[0].Title != "绝命毒师" || tv[0].MediaType != "tv" {
		t.Fatalf("get hot tv failed: %v, %+v", err, tv)
	}

	// 3. Top250
	top, err := client.GetTop250(ctx, 0, 20)
	if err != nil || len(top) != 1 || top[0].Title != "肖申克的救赎" {
		t.Fatalf("get top250 failed: %v, %+v", err, top)
	}
}
