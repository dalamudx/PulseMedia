package tmdb_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestMovieIdentitySearchSeparatesBrowsePageFromCompleteEvidence(t *testing.T) {
	item := `{"id":550,"title":"Fight Club","original_title":"Fight Club","release_date":"1999-10-15"}`
	for _, tc := range []struct {
		name, body    string
		status, rows  int
		complete, bad bool
	}{
		{"complete", `{"page":1,"total_pages":1,"total_results":1,"results":[` + item + `]}`, 200, 1, true, false},
		{"complete_empty", `{"page":1,"total_pages":0,"total_results":0,"results":[]}`, 200, 0, true, false},
		{"browse_without_pagination", `{"results":[` + item + `]}`, 200, 1, false, false},
		{"browse_first_page", `{"page":1,"total_pages":2,"total_results":21,"results":[` + item + `]}`, 200, 1, false, false},
		{"second_page", `{"page":2,"total_pages":2,"total_results":21,"results":[` + item + `]}`, 200, 1, false, false},
		{"unknown_pagination", `{"page":null,"total_pages":1,"total_results":1,"results":[` + item + `]}`, 200, 1, false, false},
		{"inconsistent_count", `{"page":1,"total_pages":1,"total_results":2,"results":[` + item + `]}`, 200, 1, false, false},
		{"negative_pages", `{"page":1,"total_pages":-1,"total_results":1,"results":[` + item + `]}`, 200, 1, false, false},
		{"missing_results", `{}`, 200, 0, false, true},
		{"null_results", `{"results":null}`, 200, 0, false, true},
		{"zero_id", `{"results":[{"id":0,"title":"Fight Club"}]}`, 200, 0, false, true},
		{"wrong_type", `{"results":[{"id":550,"media_type":"tv","title":"Fight Club"}]}`, 200, 0, false, true},
		{"impossible_date", `{"results":[{"id":550,"title":"Fight Club","release_date":"1999-99-99"}]}`, 200, 0, false, true},
		{"unconditional_304", "", 304, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/search/movie" || r.URL.Query().Get("query") != "Fight Club" || r.URL.Query().Get("year") != "1999" || r.Header.Get("If-None-Match") != "" {
					t.Errorf("invalid search request")
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL))
			search, err := client.SearchMovieIdentityCandidates(context.Background(), "Fight Club", 1999)
			if tc.bad {
				if !errors.Is(err, ports.ErrBadResponse) || search.Complete || len(search.Candidates) != 0 {
					t.Fatalf("bad response promoted: %#v err=%v", search, err)
				}
			} else {
				if err != nil || search.Complete != tc.complete || len(search.Candidates) != tc.rows {
					t.Fatalf("identity search=%#v err=%v", search, err)
				}
				browse, err := client.SearchMovies(context.Background(), "Fight Club", 1999)
				if err != nil || len(browse) != tc.rows {
					t.Fatalf("browse page lost: %#v err=%v", browse, err)
				}
				if tc.rows > 0 && (browse[0].TMDBID != 550 || browse[0].MediaType != "movie" || browse[0].Year != 1999) {
					t.Fatalf("native movie shape lost: %#v", browse[0])
				}
			}
		})
	}
}
