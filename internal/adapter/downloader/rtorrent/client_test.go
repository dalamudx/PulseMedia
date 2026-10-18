package rtorrent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/rtorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestRTorrentXMLRPCInteraction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><methodResponse><params><param><value><string>0.9.8</string></value></param></params></methodResponse>`)
	}))
	defer server.Close()

	client := rtorrent.NewClient(server.URL, server.Client())
	ctx := context.Background()

	// 1. Test XML-RPC version probe
	if err := client.Test(ctx); err != nil {
		t.Fatalf("test rtorrent failed: %v", err)
	}

	// 2. Add Torrent
	err := client.AddTorrent(ctx, ports.AddTorrentParam{
		InfoHash: "abc123hash",
		SavePath: "/downloads",
	})
	if err != nil {
		t.Fatalf("add torrent failed: %v", err)
	}

	// 3. Get Torrent
	info, err := client.GetTorrent(ctx, "abc123hash")
	if err != nil || info.State != "downloading" {
		t.Fatalf("get torrent failed: %v", err)
	}
}
