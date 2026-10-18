package qbittorrent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestQBittorrentLifecycleAndReconciliation(t *testing.T) {
	// Mock qBittorrent server
	authenticated := false
	torrents := make(map[string]map[string]any)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Login
		if r.URL.Path == "/api/v2/auth/login" {
			_ = r.ParseForm()
			if r.FormValue("username") == "admin" && r.FormValue("password") == "adminadmin" {
				authenticated = true
				http.SetCookie(w, &http.Cookie{Name: "SID", Value: "session-123"})
				_, _ = fmt.Fprint(w, "Ok.")
				return
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, "Fails.")
			return
		}

		if !authenticated {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		// 2. Add torrent
		if r.URL.Path == "/api/v2/torrents/add" {
			_ = r.ParseMultipartForm(10 * 1024 * 1024)
			savePath := r.FormValue("savepath")
			tags := r.FormValue("tags")
			urls := r.FormValue("urls")

			hash := "9a3f12c45b6d7e8f0123456789abcdef01234567"
			torrents[hash] = map[string]any{
				"hash":         hash,
				"name":         "Fight Club",
				"save_path":    savePath,
				"content_path": savePath + "/Fight Club (1999)/Fight Club.mkv",
				"progress":     0.5,
				"size":         8589934592,
				"downloaded":   4294967296,
				"state":        "downloading",
				"tags":         tags,
				"url":          urls,
			}
			_, _ = fmt.Fprint(w, "Ok.")
			return
		}

		// 3. Info
		if r.URL.Path == "/api/v2/torrents/info" {
			hash := r.URL.Query().Get("hashes")
			w.Header().Set("Content-Type", "application/json")
			if t, ok := torrents[hash]; ok {
				_, _ = fmt.Fprintf(w, `[{
					"hash": %q,
					"name": %q,
					"save_path": %q,
					"content_path": %q,
					"progress": %f,
					"size": %d,
					"downloaded": %d,
					"state": %q,
					"tags": %q
				}]`, t["hash"], t["name"], t["save_path"], t["content_path"], t["progress"], t["size"], t["downloaded"], t["state"], t["tags"])
				return
			}
			_, _ = fmt.Fprint(w, "[]")
			return
		}

		// 4. Delete
		if r.URL.Path == "/api/v2/torrents/delete" {
			_ = r.ParseForm()
			hash := r.FormValue("hashes")
			delete(torrents, hash)
			_, _ = fmt.Fprint(w, "Ok.")
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	client, err := qbittorrent.NewClient(server.URL, "admin", "adminadmin", nil)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 1. Add torrent
	param := ports.AddTorrentParam{
		InfoHash:     "9A3F12C45B6D7E8F0123456789ABCDEF01234567",
		TorrentBytes: []byte("synthetic torrent payload"),
		SavePath:     "/downloads/movies",
		Tags:         "pulsemedia,pulsemedia:op:op-1234",
	}

	if err := client.AddTorrent(ctx, param); err != nil {
		t.Fatalf("add torrent failed: %v", err)
	}

	// 2. Query torrent
	info, err := client.GetTorrent(ctx, "9a3f12c45b6d7e8f0123456789abcdef01234567")
	if err != nil {
		t.Fatalf("get torrent failed: %v", err)
	}
	if info.SavePath != "/downloads/movies" || info.Progress != 0.5 {
		t.Fatalf("unexpected info: %+v", info)
	}

	// 3. Remove torrent
	if err := client.RemoveTorrent(ctx, "9a3f12c45b6d7e8f0123456789abcdef01234567", false); err != nil {
		t.Fatalf("remove torrent failed: %v", err)
	}

	// 4. Query after remove returns ErrNotFound
	_, err = client.GetTorrent(ctx, "9a3f12c45b6d7e8f0123456789abcdef01234567")
	if err != ports.ErrNotFound {
		t.Fatalf("expected ErrNotFound after removal, got: %v", err)
	}
}
