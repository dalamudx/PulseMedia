package media_test

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
)

func movieE2ESource(t *testing.T) (*httptest.Server, []byte, string, *atomic.Int32) {
	t.Helper()
	name := "Fight.Club.1999.1080p.mkv"
	payload := []byte("REAL_MOVIE_VIDEO_BYTES_550_PAYLOAD")
	piece := sha1.Sum(payload)
	info := append([]byte(fmt.Sprintf("d6:lengthi%de4:name%d:%s12:piece lengthi16384e6:pieces20:", len(payload), len(name), name)), piece[:]...)
	info = append(info, 'e')
	digest := sha1.Sum(info)
	raw := append([]byte("d4:info"), info...)
	raw = append(raw, 'e')
	calls := &atomic.Int32{}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dl/550.torrent" || r.URL.Query().Get("passkey") != "fixture-source-key" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(source.Close)
	return source, raw, hex.EncodeToString(digest[:]), calls
}

func configureMovieE2ESource(repo *downloadstore.Repository, source *httptest.Server) {
	repo.SetTorrentSource(func(_ string, endpoint, key string) ports.IndexerClient {
		if endpoint != source.URL || key != "fixture-source-key" {
			return nil
		}
		return torznab.NewClient(endpoint, key, source.Client())
	}, torrent.Inspector{})
}

func sameMovieMetainfo(got, expected []byte) bool { return bytes.Equal(got, expected) }
