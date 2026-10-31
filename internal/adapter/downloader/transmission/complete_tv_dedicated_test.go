package transmission

import (
	"context"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestDedicatedByteSendersExactAndNoReplay(t *testing.T) {
	for _, kind := range []string{"complete-tv", "movie-upgrade"} {
		for _, mode := range []string{"ack", "session-409", "wrong-hash", "duplicate-ack", "lost-ack"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				p := automaticTVByteParam()
				var calls, accepted atomic.Int32
				c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					checkAutomaticTVAddRequest(t, r, p)
					if mode == "session-409" && r.Header.Get("X-Transmission-Session-Id") == "" {
						w.Header().Set("X-Transmission-Session-Id", "dedicated-session")
						w.WriteHeader(409)
						return
					}
					accepted.Add(1)
					if mode == "lost-ack" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
						return
					}
					ack := automaticTVAddedACK(p.InfoHash)
					if mode == "wrong-hash" {
						ack = strings.Replace(ack, p.InfoHash, strings.Repeat("f", 40), 1)
					}
					if mode == "duplicate-ack" {
						ack = strings.Replace(ack, "torrent-added", "torrent-duplicate", 1)
					}
					_, _ = io.WriteString(w, ack)
				})
				sender := c.CompleteTVByteSender()
				if kind == "movie-upgrade" {
					sender = c.MovieUpgradeByteSender()
				}
				err := sender.AddTorrent(context.Background(), p)
				wantOK := mode == "ack" || mode == "session-409"
				wantCalls := int32(1)
				if mode == "session-409" {
					wantCalls = 2
				}
				if (err == nil) != wantOK || accepted.Load() != 1 || calls.Load() != wantCalls {
					t.Fatal("ACK/replay fence", err, calls.Load(), accepted.Load())
				}
			})
		}
	}
}

func TestDedicatedManifestNativeFactsAndNoExport(t *testing.T) {
	for _, mode := range []string{"complete", "checking", "near-one", "unwanted", "duplicate-json"} {
		t.Run(mode, func(t *testing.T) {
			task := automaticTVTestTask()
			if mode == "checking" {
				task["status"] = 2
			}
			if mode == "near-one" {
				task["totalSize"] = int64(math.MaxInt64)
				task["haveValid"] = int64(math.MaxInt64 - 1)
			}
			body := automaticTVTestBody(task)
			if mode == "duplicate-json" {
				body = strings.Replace(body, `"haveValid":`, `"haveValid":0,"haveValid":`, 1)
			}
			c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
			reader := c.CompleteTVManifestTrackingObserver()
			if _, ok := reader.(ports.CompleteTVTrackingClient); ok {
				t.Fatal("fabricated native export capability")
			}
			if _, ok := reader.(ports.DownloadClient); ok || reflect.TypeOf(reader).NumMethod() != 2 {
				t.Fatal("read capability exposes writes")
			}
			rows, err := reader.ListTorrents(t.Context(), automaticTVTestTag)
			if mode == "duplicate-json" {
				if err == nil || rows != nil {
					t.Fatal("accepted duplicate JSON")
				}
				return
			}
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			if (mode == "checking" || mode == "near-one") && rows[0].Progress >= 1 {
				t.Fatal("rounded or rechecking completion")
			}
			if mode == "complete" && (rows[0].State != "uploading" || rows[0].Progress != 1) {
				t.Fatal(rows[0])
			}
			if mode == "unwanted" {
				files, e := reader.GetTorrentFiles(t.Context(), automaticTVTestHash)
				if e != nil || len(files) != 2 || files[1].Name != "Show/extra.txt" || files[1].SizeBytes != 0 {
					t.Fatal(files, e)
				}
			}
		})
	}
}
