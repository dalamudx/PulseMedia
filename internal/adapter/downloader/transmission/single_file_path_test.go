package transmission

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTorrentSingleFileContentPath(t *testing.T) {
	file := func(row map[string]any) map[string]any { return row["files"].([]map[string]any)[0] }
	cases := []struct {
		name   string
		change func(map[string]any)
		want   string
		bad    bool
	}{
		{name: "native-singleton", want: "/downloads/actual.mkv"},
		{name: "nested-native-name", change: func(r map[string]any) { file(r)["name"] = "folder/actual.mkv" }, want: "/downloads/folder/actual.mkv"},
		{name: "root-directory", change: func(r map[string]any) { r["downloadDir"] = "/" }, want: "/actual.mkv"},
		{name: "missing-inventory", change: func(r map[string]any) { delete(r, "files"); delete(r, "fileStats") }},
		{name: "missing-stats", change: func(r map[string]any) { delete(r, "fileStats") }},
		{name: "multiple-files", change: func(r map[string]any) {
			r["files"] = append(r["files"].([]map[string]any), map[string]any{"name": "other.mkv", "length": 100, "bytesCompleted": 100})
			r["fileStats"] = append(r["fileStats"].([]map[string]any), map[string]any{"wanted": true, "bytesCompleted": 100})
			r["totalSize"] = 200
			r["haveValid"] = 200
		}},
		{name: "unknown-valid-bytes", change: func(r map[string]any) { delete(r, "haveValid") }},
		{name: "partial-task", change: func(r map[string]any) { r["haveValid"] = 50; r["percentDone"] = 0.5 }},
		{name: "checking", change: func(r map[string]any) { r["status"] = 2 }},
		{name: "size-mismatch", change: func(r map[string]any) { r["totalSize"] = 101; r["haveValid"] = 101 }},
		{name: "partial-file", change: func(r map[string]any) {
			file(r)["bytesCompleted"] = 50
			r["fileStats"].([]map[string]any)[0]["bytesCompleted"] = 50
		}},
		{name: "path-escape", change: func(r map[string]any) { file(r)["name"] = "../escape.mkv" }, bad: true},
		{name: "absolute-file", change: func(r map[string]any) { file(r)["name"] = "/escape.mkv" }, bad: true},
		{name: "unclean-file", change: func(r map[string]any) { file(r)["name"] = "folder/../actual.mkv" }, bad: true},
		{name: "backslash", change: func(r map[string]any) { file(r)["name"] = "folder\\actual.mkv" }, bad: true},
		{name: "missing-name", change: func(r map[string]any) { delete(file(r), "name") }, bad: true},
		{name: "inconsistent-stats", change: func(r map[string]any) { r["fileStats"].([]map[string]any)[0]["bytesCompleted"] = 90 }, bad: true},
		{name: "negative-length", change: func(r map[string]any) { file(r)["length"] = -1 }, bad: true},
		{name: "relative-root", change: func(r map[string]any) { r["downloadDir"] = "downloads" }, bad: true},
		{name: "unclean-root", change: func(r map[string]any) { r["downloadDir"] = "/downloads/../elsewhere" }, bad: true},
		{name: "wrong-hash", change: func(r map[string]any) { r["hashString"] = strings.Repeat("b", 40) }, bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash := strings.Repeat("a", 40)
			row := map[string]any{"hashString": hash, "name": "unrelated-display-name.mkv", "downloadDir": "/downloads", "percentDone": 1, "totalSize": 100, "haveValid": 100, "status": 6, "labels": []string{"pulsemedia"}, "files": []map[string]any{{"name": "actual.mkv", "length": 100, "bytesCompleted": 100}}, "fileStats": []map[string]any{{"wanted": true, "bytesCompleted": 100}}}
			if tc.change != nil {
				tc.change(row)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					Method    string `json:"method"`
					Tag       int    `json:"tag"`
					Arguments struct {
						Fields []string `json:"fields"`
						IDs    []string `json:"ids"`
					} `json:"arguments"`
				}
				if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || request.Method != "torrent-get" || len(request.Arguments.IDs) != 1 || request.Arguments.IDs[0] != hash || !slices.Contains(request.Arguments.Fields, "files") || !slices.Contains(request.Arguments.Fields, "fileStats") {
					t.Error("inventory not requested with original identity")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrents": []map[string]any{row}}, "tag": request.Tag})
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.GetTorrent(t.Context(), hash)
			if tc.bad {
				if !errors.Is(err, ports.ErrBadResponse) {
					t.Fatalf("expected rejected native evidence, got %v", err)
				}
			} else if err != nil || got == nil || got.ContentPath != tc.want {
				t.Fatalf("content path: got %+v error %v, want %q", got, err, tc.want)
			}
			if calls.Load() != 1 {
				t.Fatal("path resolution made extra requests", calls.Load())
			}
		})
	}
}
