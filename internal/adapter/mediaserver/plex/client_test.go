package plex_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/plex"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestPlexClientLifecycle(t *testing.T) {
	refreshedURLs := make([]string, 0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Plex-Token")
		if token != "valid-plex-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// 1. Identity / test endpoint
		if r.URL.Path == "/identity" {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"MediaContainer": {"claimed": true}}`)
			return
		}

		// 2. Sections endpoint
		if r.URL.Path == "/library/sections" {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{
				"MediaContainer": {
					"Directory": [
						{
							"key": "1",
							"type": "movie",
							"title": "Movies",
							"Location": [{"path": "/media/movies"}]
						},
						{
							"key": "2",
							"type": "show",
							"title": "TV Shows",
							"Location": [{"path": "/media/tv"}]
						}
					]
				}
			}`)
			return
		}

		// 3. Section refresh endpoint
		if r.URL.Path == "/library/sections/1/refresh" {
			refreshedURLs = append(refreshedURLs, r.URL.String())
			w.WriteHeader(http.StatusOK)
			return
		}

		// 4. Section items endpoint
		if r.URL.Path == "/library/sections/1/all" {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{
				"MediaContainer": {
					"Metadata": [
						{
							"ratingKey": "1001",
							"title": "Fight Club",
							"Media": [
								{
									"Part": [
										{"file": "/media/movies/Fight Club (1999)/Fight Club.mkv"}
									]
								}
							]
						}
					]
				}
			}`)
			return
		}
		if r.URL.Path == "/library/sections/2/all" {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"MediaContainer": {"Metadata": []}}`)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	client := plex.NewClient(server.URL, "valid-plex-token", nil)
	ctx := context.Background()

	// 1. Test connection
	if err := client.Test(ctx); err != nil {
		t.Fatalf("test plex connection failed: %v", err)
	}

	// 2. Refresh paths (triggers section 1 partial refresh)
	moviePath := "/media/movies/Fight Club (1999)/Fight Club.mkv"
	if err := client.RefreshPaths(ctx, []string{moviePath}); err != nil {
		t.Fatalf("refresh paths failed: %v", err)
	}
	if len(refreshedURLs) != 1 {
		t.Fatalf("expected 1 refresh call, got: %d (%+v)", len(refreshedURLs), refreshedURLs)
	}

	// 3. FindByPath returns ratingKey
	ratingKey, found, err := client.FindByPath(ctx, moviePath)
	if err != nil || !found || ratingKey != "1001" {
		t.Fatalf("find by path failed: key=%s, found=%v, err=%v", ratingKey, found, err)
	}

	// 4. FindByPath on missing file returns false
	_, foundMissing, err := client.FindByPath(ctx, "/media/movies/Nonexistent.mkv")
	if err != nil || foundMissing {
		t.Fatalf("expected missing file not found")
	}

	// 5. Unauthorized token returns ErrAuthRequired
	badClient := plex.NewClient(server.URL, "bad-token", nil)
	err = badClient.Test(ctx)
	if !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatalf("expected ErrAuthRequired for invalid token, got: %v", err)
	}
}
