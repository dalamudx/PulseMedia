package transmission

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestNativeDownloadRateHTTP(t *testing.T) {
	const hash = "abcdef0123456789abcdef0123456789abcdef01"
	const tag = "pulsemedia:op:12345678-1234-4234-8234-123456789abc"
	zero, ordinary, maximum := int64(0), int64(123456), int64(math.MaxInt64)
	for _, tc := range []struct {
		name string
		raw  string
		want *int64
	}{
		{"missing", "", nil},
		{"null", "null", nil},
		{"negative", "-1", nil},
		{"string", `"123456"`, nil},
		{"bool", "true", nil},
		{"object", `{}`, nil},
		{"array", `[]`, nil},
		{"nonintegral", "1.5", nil},
		{"exponent", "1e3", nil},
		{"overflow", "9223372036854775808", nil},
		{"underflow", "-9223372036854775809", nil},
		{"zero", "0", &zero},
		{"ordinary", "123456", &ordinary},
		{"max-int64", "9223372036854775807", &maximum},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{"hashString": hash, "name": "episode", "downloadDir": "/downloads/TV", "percentDone": 0.4, "totalSize": 100, "haveValid": 40, "status": 4, "labels": []string{tag}, "error": 0}
			if tc.raw != "" {
				row["rateDownload"] = json.RawMessage(tc.raw)
			}
			body, err := json.Marshal(map[string]any{"result": "success", "arguments": map[string]any{"torrents": []map[string]any{row}}})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				user, password, ok := r.BasicAuth()
				if !ok || user != "rate-user" || password != "rate-password" || r.Method != http.MethodPost || r.URL.Path != "/transmission/rpc" {
					t.Error("invalid authenticated native RPC")
				}
				var request struct {
					Method    string `json:"method"`
					Arguments struct {
						Fields []string `json:"fields"`
						IDs    []string `json:"ids"`
					} `json:"arguments"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Method != "torrent-get" {
					t.Error("unexpected RPC", request.Method)
				}
				found := false
				for _, field := range request.Arguments.Fields {
					if field == "rateDownload" {
						found = true
					}
				}
				if !found {
					t.Error("native RPC did not request rateDownload", request.Arguments.Fields)
				}
				if call == 1 {
					if !reflect.DeepEqual(request.Arguments.IDs, []string{hash}) {
						t.Error("wrong hash lookup", request.Arguments.IDs)
					}
				} else if len(request.Arguments.IDs) != 0 {
					t.Error("list query unexpectedly restricted", request.Arguments.IDs)
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "rate-user", "rate-password", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			info, err := client.GetTorrent(ctx, hash)
			if err != nil || info == nil {
				t.Fatalf("GetTorrent: %v, %v", info, err)
			}
			list, err := client.ListTorrents(ctx, tag)
			if err != nil || len(list) != 1 {
				t.Fatalf("ListTorrents: %v, %v", list, err)
			}
			tasks, err := client.AutomaticTVObserver().ListAutomaticTVTasks(ctx, tag)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("ListAutomaticTVTasks: %v, %v", tasks, err)
			}
			for _, got := range []*ports.TorrentInfo{info, list[0]} {
				if got.InfoHash != hash || got.Name != "episode" || got.SavePath != "/downloads/TV" || got.Progress != 0.4 || got.TotalBytes != 100 || got.DownloadedBytes != 40 || got.State != "downloading" || got.Tags != tag {
					t.Fatalf("native identity/progress changed: %+v", got)
				}
				if !reflect.DeepEqual(got.DownloadRateBytesPerSecond, tc.want) {
					t.Fatalf("rate = %v, want %v", got.DownloadRateBytesPerSecond, tc.want)
				}
			}
			task := tasks[0]
			if task.InfoHash != hash || task.SavePath != "/downloads/TV" || task.ContentPath != "" || task.TotalBytes != 100 || task.VerifiedBytes != 40 || task.Complete || task.State != "downloading" || !reflect.DeepEqual(task.Tags, []string{tag}) {
				t.Fatalf("native completion evidence changed: %+v", task)
			}
			if !reflect.DeepEqual(task.DownloadRateBytesPerSecond, tc.want) {
				t.Fatalf("automatic rate = %v, want %v", task.DownloadRateBytesPerSecond, tc.want)
			}
			if got := calls.Load(); got != 3 {
				t.Fatalf("native request count = %d, want 3", got)
			}
		})
	}
}

func TestDownloadRateExcludedFromPortJSON(t *testing.T) {
	info := ports.TorrentInfo{InfoHash: "abcdef0123456789abcdef0123456789abcdef01", Name: "episode", SavePath: "/downloads/TV", Progress: 0.4, TotalBytes: 100, DownloadedBytes: 40, State: "downloading", Tags: "pulsemedia:op:12345678-1234-4234-8234-123456789abc"}
	task := ports.AutomaticTVRemoteTask{InfoHash: info.InfoHash, SavePath: info.SavePath, State: info.State, Tags: []string{info.Tags}, TotalBytes: 100, VerifiedBytes: 40}
	beforeInfo, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	beforeTask, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	for _, rate := range []int64{0, 123456, math.MaxInt64} {
		info.DownloadRateBytesPerSecond = &rate
		task.DownloadRateBytesPerSecond = &rate
		afterInfo, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		afterTask, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(beforeInfo, afterInfo) || !bytes.Equal(beforeTask, afterTask) {
			t.Fatalf("speed %d changed sealed JSON: %s / %s", rate, afterInfo, afterTask)
		}
	}
}
