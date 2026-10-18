package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddAcknowledgementRequiresSingleMatchingSuccess(t *testing.T) {
	hash := strings.Repeat("a", 40)
	valid := `{"added_torrent_ids":["` + hash + `"],"failure_count":0,"pending_count":0,"success_count":1}`
	cases := []struct {
		name, body string
		ok         bool
	}{
		{"legacy", "Ok.", true}, {"json", valid, true}, {"case-normalized-hash", strings.ReplaceAll(valid, hash, strings.ToUpper(hash)), true},
		{"empty", "", false}, {"failed-marker", "Fails.", false}, {"pending", strings.Replace(valid, `"pending_count":0`, `"pending_count":1`, 1), false},
		{"failed", strings.Replace(valid, `"failure_count":0`, `"failure_count":1`, 1), false},
		{"zero-success", strings.Replace(valid, `"success_count":1`, `"success_count":0`, 1), false},
		{"wrong-hash", strings.Replace(valid, hash, strings.Repeat("b", 40), 1), false},
		{"missing-count", strings.Replace(valid, `"pending_count":0,`, "", 1), false},
		{"null-count", strings.Replace(valid, `"failure_count":0`, `"failure_count":null`, 1), false},
		{"fractional-count", strings.Replace(valid, `"success_count":1`, `"success_count":1.5`, 1), false},
		{"repeated-identity", strings.Replace(valid, `["`+hash+`"]`, `["`+hash+`","`+hash+`"]`, 1), false},
		{"duplicate-count", strings.Replace(valid, `"failure_count":0`, `"failure_count":1,"failure_count":0`, 1), false},
		{"duplicate-count-alias", strings.Replace(valid, `"failure_count":0`, `"Failure_Count":1,"failure_count":0`, 1), false},
		{"trailing", valid + `{}`, false}, {"array", "[" + valid + "]", false}, {"too-large", strings.Repeat(" ", 4096) + valid, false},
	}
	for _, special := range []bool{false, true} {
		name := "ordinary"
		if special {
			name = "frozen-byte-sender"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					adds := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/api/v2/torrents/add" || r.Method != http.MethodPost {
							t.Error("unexpected confirmation probe")
							http.NotFound(w, r)
							return
						}
						_, _ = io.Copy(io.Discard, r.Body)
						adds++
						_, _ = w.Write([]byte(tc.body))
					}))
					defer server.Close()
					client, e := NewClient(server.URL, "", "", server.Client())
					if e != nil {
						t.Fatal(e)
					}
					client.loggedIn = true
					var sender ports.DownloadClient = client
					if special {
						sender = client.CompleteTVByteSender()
					}
					e = sender.AddTorrent(context.Background(), ports.AddTorrentParam{InfoHash: hash, TorrentBytes: []byte("fixture"), SavePath: "/library", Tags: "operation"})
					if tc.ok {
						if e != nil {
							t.Fatalf("valid confirmation rejected: %v", e)
						}
					} else if !errors.Is(e, ports.ErrBadResponse) {
						t.Fatalf("uncertain confirmation accepted: %v", e)
					}
					if adds != 1 {
						t.Fatalf("Add was replayed: %d", adds)
					}
				})
			}
		})
	}
	// A v2-only magnet can provide its identity without a separate InfoHash hint.
	v2 := strings.Repeat("b", 64)
	raw, _ := json.Marshal(map[string]any{"added_torrent_ids": []string{v2}, "failure_count": 0, "pending_count": 0, "success_count": 1})
	if e := confirmAddAcknowledgement(raw, ports.AddTorrentParam{DownloadURL: "magnet:?xt=urn:btmh:1220" + v2}); e != nil {
		t.Fatal("v2 magnet identity was lost")
	}
	if e := confirmAddAcknowledgement([]byte(valid), ports.AddTorrentParam{TorrentBytes: []byte("fixture")}); !errors.Is(e, ports.ErrBadResponse) {
		t.Fatal("JSON byte acknowledgement accepted without a frozen identity")
	}
}
