package transmission_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTransmissionClientRPCLifecycle(t *testing.T) {
	sessionID := "tr-session-token-xyz"
	torrents := make(map[string]map[string]any)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Check Session ID header (409 Conflict if missing, as standard Transmission behaviour)
		if r.Header.Get("X-Transmission-Session-Id") != sessionID {
			w.Header().Set("X-Transmission-Session-Id", sessionID)
			w.WriteHeader(http.StatusConflict)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// 2. Parse method
		var req struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "session-get":
			_, _ = fmt.Fprint(w, `{"result": "success", "arguments": {"version": "4.0.5"}}`)
			return
		case "torrent-add":
			torrents["9a3f12c45b6d7e8f0123456789abcdef01234567"] = map[string]any{
				"hash": req.Arguments["filename"],
			}
			_, _ = fmt.Fprint(w, `{"result": "success", "arguments": {"torrent-added": {"hashString": "9a3f12c45b6d7e8f0123456789abcdef01234567", "name": "Fight Club"}}}`)
			return
		case "torrent-get":
			_, _ = fmt.Fprint(w, `{
				"result": "success",
				"arguments": {
					"torrents": [
						{
							"hashString": "9a3f12c45b6d7e8f0123456789abcdef01234567",
							"name": "Fight Club",
							"downloadDir": "/downloads/movies",
							"percentDone": 0.75,
							"totalSize": 8000000000,
							"status": 4
						}
					]
				}
			}`)
			return
		case "torrent-remove":
			delete(torrents, "9a3f12c45b6d7e8f0123456789abcdef01234567")
			_, _ = fmt.Fprint(w, `{"result": "success"}`)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	client, err := transmission.NewClient(server.URL, "admin", "admin", server.Client())
	if err != nil {
		t.Fatalf("failed to create transmission client: %v", err)
	}

	ctx := context.Background()

	// 1. Test session-get probe (verifies 409 handshake and negotiation)
	if err := client.Test(ctx); err != nil {
		t.Fatalf("test transmission connection failed: %v", err)
	}

	// 2. Add torrent
	param := ports.AddTorrentParam{
		InfoHash:     "9a3f12c45b6d7e8f0123456789abcdef01234567",
		TorrentBytes: []byte("synthetic torrent payload"),
		SavePath:     "/downloads/movies",
	}
	if err := client.AddTorrent(ctx, param); err != nil {
		t.Fatalf("add torrent failed: %v", err)
	}

	// 3. Get torrent
	info, err := client.GetTorrent(ctx, "9a3f12c45b6d7e8f0123456789abcdef01234567")
	if err != nil || info.Progress != 0.75 || info.SavePath != "/downloads/movies" {
		t.Fatalf("get torrent failed: %v, %+v", err, info)
	}

	// 4. Remove torrent
	if err := client.RemoveTorrent(ctx, "9a3f12c45b6d7e8f0123456789abcdef01234567", false); err != nil {
		t.Fatalf("remove torrent failed: %v", err)
	}
}
