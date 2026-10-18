package jellyfin_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/jellyfin"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestJellyfinExactPathProof(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		found, bad bool
	}{
		{"valid", `{"Items":[{"Id":"one","Path":"/media/movie.mkv"}],"TotalRecordCount":1}`, true, false},
		{"empty", `{"Items":[],"TotalRecordCount":0}`, false, false},
		{"unrelated", `{"Items":[{"Id":"other","Path":"/other/movie.mkv"}],"TotalRecordCount":1}`, false, false},
		{"match-among-unrelated", `{"Items":[{"Id":"other","Path":"/other/movie.mkv"},{"Id":"one","Path":"/media/movie.mkv"}],"TotalRecordCount":2}`, true, false},
		{"missingpath", `{"Items":[{"Id":"one"}],"TotalRecordCount":1}`, false, true},
		{"missingid", `{"Items":[{"Path":"/media/movie.mkv"}],"TotalRecordCount":1}`, false, true},
		{"ambiguous", `{"Items":[{"Id":"one","Path":"/media/movie.mkv"},{"Id":"two","Path":"/media/movie.mkv"}],"TotalRecordCount":2}`, false, true},
		{"duplicate-id", `{"Items":[{"Id":"one","Path":"/media/movie.mkv"},{"Id":"one","Path":"/other/movie.mkv"}],"TotalRecordCount":2}`, false, true},
		{"incomplete", `{"Items":[{"Id":"one","Path":"/media/movie.mkv"}],"TotalRecordCount":2}`, false, true},
		{"missing-total", `{"Items":[{"Id":"one","Path":"/media/movie.mkv"}]}`, false, true},
		{"missingitems", `{}`, false, true}, {"nullitems", `{"Items":null,"TotalRecordCount":0}`, false, true},
		{"concatenated", `{"Items":[],"TotalRecordCount":0}{"Items":[]}`, false, true},
		{"oversized", `{"Items":[],"TotalRecordCount":0,"ignored":"` + strings.Repeat("x", 8<<20) + `"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("Fields") != "Path" || q.Has("Path") || q.Get("Recursive") != "true" || q.Get("MediaTypes") != "Video" || q.Get("EnableTotalRecordCount") != "true" {
					t.Error("expected supported complete recursive video inventory query")
				}
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			id, found, err := jellyfin.NewClient(server.URL, "test-key", nil).FindByPath(context.Background(), "/media/movie.mkv")
			if found != tc.found || tc.bad && !errors.Is(err, ports.ErrBadResponse) || !tc.bad && err != nil || tc.found && id != "one" {
				t.Fatal(id, found, err)
			}
		})
	}
}
