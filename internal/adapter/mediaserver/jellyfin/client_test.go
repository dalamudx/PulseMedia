package jellyfin_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/jellyfin"
)

func TestJellyfinRefreshAndFindByPath(t *testing.T) {
	refreshedPaths := make([]string, 0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token != `MediaBrowser Token="valid-token"` {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// 1. Refresh paths
		if r.URL.Path == "/Library/Media/Updated" {
			w.WriteHeader(http.StatusNoContent)
			refreshedPaths = append(refreshedPaths, "refreshed")
			return
		}

		// 2. Find by path
		if r.URL.Path == "/Items" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"Items": [{"Id": "item-12345", "Path":"/movies/Fight Club (1999)/Fight Club.mkv"}],"TotalRecordCount":1}`)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	client := jellyfin.NewClient(server.URL, "valid-token", nil)
	ctx := context.Background()

	// 1. Refresh paths
	err := client.RefreshPaths(ctx, []string{"/movies/Fight Club (1999)/Fight Club.mkv"})
	if err != nil {
		t.Fatalf("refresh paths failed: %v", err)
	}
	if len(refreshedPaths) != 1 {
		t.Fatal("expected refresh call to succeed")
	}

	// 2. Find existing item
	id, found, err := client.FindByPath(ctx, "/movies/Fight Club (1999)/Fight Club.mkv")
	if err != nil || !found || id != "item-12345" {
		t.Fatalf("expected item found: id=%s, found=%v, err=%v", id, found, err)
	}

	// 3. Find non-existing item
	_, foundMissing, err := client.FindByPath(ctx, "/movies/Missing.mkv")
	if err != nil || foundMissing {
		t.Fatalf("expected missing item to return found=false")
	}
}
