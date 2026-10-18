package transmission

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddPersistsOperationLabels(t *testing.T) {
	labels := make(chan []string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string `json:"method"`
			Arguments struct {
				Labels []string `json:"labels"`
			} `json:"arguments"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "torrent-add" {
			t.Error("invalid Add RPC")
		}
		labels <- req.Arguments.Labels
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "", "", nil)
	hash := strings.Repeat("a", 40)
	if err := client.AddTorrent(context.Background(), ports.AddTorrentParam{InfoHash: hash, DownloadURL: "magnet:?xt=urn:btih:" + hash, Tags: " pulsemedia:op:one, category ,, "}); err != nil {
		t.Fatal(err)
	}
	got := <-labels
	if len(got) != 2 || got[0] != "pulsemedia:op:one" || got[1] != "category" {
		t.Fatalf("native labels lost: %v", got)
	}
}

func TestListUsesExactLabelsAndActualBytes(t *testing.T) {
	const size int64 = 8000000003
	const wanted = "pulsemedia:op:one"
	hash := strings.Repeat("a", 40)
	for _, hasBytes := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "trusted"}[hasBytes], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Arguments struct {
						Fields []string `json:"fields"`
					} `json:"arguments"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("invalid observation RPC")
				}
				for _, needed := range []string{"labels", "haveValid", "status"} {
					found := false
					for _, field := range req.Arguments.Fields {
						if field == needed {
							found = true
						}
					}
					if !found {
						t.Errorf("missing observation field %s", needed)
					}
				}
				own := map[string]any{"hashString": hash, "name": "self", "downloadDir": "/downloads", "percentDone": 1, "totalSize": size, "status": 6, "labels": []string{wanted, "category"}}
				if hasBytes {
					own["haveValid"] = size
				}
				other := map[string]any{"hashString": strings.Repeat("b", 40), "percentDone": 1, "totalSize": size, "status": 6, "labels": []string{wanted + "-other"}, "haveValid": size}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrents": []any{own, other}}})
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "", "", nil)
			got, err := client.ListTorrents(context.Background(), wanted)
			if err != nil || len(got) != 1 || got[0].InfoHash != hash || got[0].Tags != wanted+",category" || got[0].State != "seeding" {
				t.Fatalf("operation isolation failed: %+v %v", got, err)
			}
			expected := int64(0)
			if hasBytes {
				expected = size
			}
			if got[0].DownloadedBytes != expected {
				t.Fatal("bytes were guessed or rounded")
			}
			absent, err := client.ListTorrents(context.Background(), "absent")
			if err != nil || absent == nil || len(absent) != 0 {
				t.Fatal("unrelated task matched operation tag")
			}
		})
	}
}

func TestUnknownTorrentListCannotProveAbsence(t *testing.T) {
	for _, body := range []string{`{"result":"success"}`, `{"result":"success","arguments":{}}`, `{"result":"success","arguments":{"torrents":null}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		client, _ := NewClient(server.URL, "", "", nil)
		_, listErr := client.ListTorrents(context.Background(), "operation")
		_, getErr := client.GetTorrent(context.Background(), strings.Repeat("a", 40))
		server.Close()
		if !errors.Is(listErr, ports.ErrBadResponse) || !errors.Is(getErr, ports.ErrBadResponse) {
			t.Fatalf("unknown response became absence: list=%v get=%v", listErr, getErr)
		}
	}
}
