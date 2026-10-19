package transmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const automaticTVTestHash = "abcdef0123456789abcdef0123456789abcdef01"
const automaticTVTestTag = "pulsemedia:op:12345678-1234-4234-8234-123456789abc"

func automaticTVTestTask() map[string]any {
	return map[string]any{"hashString": strings.ToUpper(automaticTVTestHash), "downloadDir": "/downloads/TV", "labels": []string{automaticTVTestTag, "other"}, "status": 6, "totalSize": int64(32), "haveValid": int64(32), "error": 0,
		"files":     []any{map[string]any{"name": "Show/episode.mkv", "length": int64(32), "bytesCompleted": int64(32)}, map[string]any{"name": "Show/extra.txt", "length": 0, "bytesCompleted": 0}},
		"fileStats": []any{map[string]any{"bytesCompleted": int64(32), "wanted": true}, map[string]any{"bytesCompleted": 0, "wanted": false}}}
}

func automaticTVTestBody(tasks ...map[string]any) string {
	if tasks == nil {
		tasks = []map[string]any{}
	}
	b, err := json.Marshal(map[string]any{"result": "success", "arguments": map[string]any{"torrents": tasks}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func automaticTVTestReader(t *testing.T, body string) ports.AutomaticTVReadClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, "fixture-user", "fixture-password", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c.AutomaticTVObserver()
}

func TestAutomaticTVNativeRequestsSessionAndAuthority(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "fixture-user" || password != "fixture-password" || r.Method != http.MethodPost || r.URL.Path != "/transmission/rpc" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect native authenticated request")
		}
		var req struct {
			Method    string                     `json:"method"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "torrent-get" {
			t.Error("capability issued non-read RPC")
		}
		var fields []string
		if json.Unmarshal(req.Arguments["fields"], &fields) != nil {
			t.Error("missing fields")
		}
		want := []string{"hashString", "downloadDir", "labels", "status", "totalSize", "haveValid", "error"}
		if raw, ok := req.Arguments["ids"]; ok {
			var ids []string
			if json.Unmarshal(raw, &ids) != nil || !reflect.DeepEqual(ids, []string{automaticTVTestHash}) {
				t.Error("wrong hash query")
			}
			want = append(want, "files", "fileStats")
		}
		if !reflect.DeepEqual(fields, want) || len(req.Arguments) > 2 {
			t.Error("incomplete or limited native query", req.Arguments)
		}
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "fixture-session")
			w.WriteHeader(409)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "fixture-session" {
			t.Error("session not reused")
		}
		_, _ = io.WriteString(w, automaticTVTestBody(automaticTVTestTask()))
	}))
	defer srv.Close()
	borrowed := srv.Client()
	c, _ := NewClient(srv.URL, "fixture-user", "fixture-password", borrowed)
	reader := c.AutomaticTVObserver()
	if _, ok := reader.(ports.DownloadClient); ok {
		t.Fatal("observation leaked mutation authority")
	}
	typ := reflect.TypeOf(reader)
	if typ.NumMethod() != 2 {
		t.Fatal("unexpected capability methods", typ)
	}
	tasks, err := reader.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
	if err != nil || len(tasks) != 1 || tasks[0].InfoHash != automaticTVTestHash || tasks[0].SavePath != "/downloads/TV" || tasks[0].ContentPath != "" || !tasks[0].Complete || tasks[0].VerifiedBytes != 32 || !reflect.DeepEqual(tasks[0].Tags, []string{automaticTVTestTag, "other"}) {
		t.Fatal(tasks, err)
	}
	files, err := reader.GetAutomaticTVFiles(context.Background(), strings.ToUpper(automaticTVTestHash))
	if err != nil || !reflect.DeepEqual(files, []ports.AutomaticTVRemoteFile{{Index: 0, Path: "Show/episode.mkv", Size: 32, CompletedBytes: 32, Wanted: true}, {Index: 1, Path: "Show/extra.txt", Size: 0, CompletedBytes: 0, Wanted: false}}) {
		t.Fatal(files, err)
	}
	if calls.Load() != 3 || borrowed.CheckRedirect != nil {
		t.Fatal("unexpected retries or borrowed-client mutation", calls.Load())
	}
}

func TestAutomaticTVTaskRequiredFieldsAndTypes(t *testing.T) {
	fields := []string{"hashString", "downloadDir", "labels", "status", "totalSize", "haveValid", "error"}
	for _, field := range fields {
		for _, kind := range []string{"missing", "null", "wrong-type", "duplicate", "wrong-case"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				task := automaticTVTestTask()
				switch kind {
				case "missing":
					delete(task, field)
				case "null":
					task[field] = nil
				case "wrong-type":
					task[field] = map[string]any{"fixture": "secret"}
				case "wrong-case":
					task[strings.ToUpper(field)] = task[field]
					delete(task, field)
				}
				body := automaticTVTestBody(task)
				if kind == "duplicate" {
					body = strings.Replace(body, `"`+field+`":`, `"`+field+`":null,"`+field+`":`, 1)
				}
				tasks, err := automaticTVTestReader(t, body).ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
				if !errors.Is(err, ports.ErrUnavailable) || tasks != nil {
					t.Fatal(tasks, err)
				}
			})
		}
	}
	for _, field := range []string{"status", "totalSize", "haveValid", "error"} {
		for _, number := range []string{"-1", "0.5", "1.0", "9223372036854775808", "1e0"} {
			t.Run(field+"/"+number, func(t *testing.T) {
				task := automaticTVTestTask()
				task[field] = json.RawMessage(number)
				if out, err := automaticTVTestReader(t, automaticTVTestBody(task)).ListAutomaticTVTasks(context.Background(), automaticTVTestTag); !errors.Is(err, ports.ErrUnavailable) || out != nil {
					t.Fatal(out, err)
				}
			})
		}
	}
}

func TestAutomaticTVTaskValidationBeforeExactFilter(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"hash", func(m map[string]any) { m["hashString"] = strings.Repeat("x", 40) }},
		{"v2", func(m map[string]any) { m["hashString"] = strings.Repeat("a", 64) }},
		{"unknown-status", func(m map[string]any) { m["status"] = 7 }},
		{"unknown-error", func(m map[string]any) { m["error"] = 4 }},
		{"valid-over-total", func(m map[string]any) { m["haveValid"] = 33 }},
		{"label-duplicate", func(m map[string]any) { m["labels"] = []string{automaticTVTestTag, automaticTVTestTag} }},
		{"label-space", func(m map[string]any) { m["labels"] = []string{" " + automaticTVTestTag} }},
		{"label-control", func(m map[string]any) { m["labels"] = []string{automaticTVTestTag + "\t"} }},
		{"label-null-element", func(m map[string]any) { m["labels"] = []any{nil} }},
	}
	for _, p := range []string{"relative", "/a/../b", "/a/./b", "/a//b", "/a/", "/a\x00b", "/a\tb", "/a\\b", "/a:b"} {
		value := p
		cases = append(cases, struct {
			name   string
			mutate func(map[string]any)
		}{"path-" + fmt.Sprintf("%q", p), func(m map[string]any) { m["downloadDir"] = value }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := automaticTVTestTask()
			tc.mutate(task)
			if out, err := automaticTVTestReader(t, automaticTVTestBody(task)).ListAutomaticTVTasks(context.Background(), automaticTVTestTag); out != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(out, err)
			}
		})
	}
	t.Run("invalid-unmatched-after-match", func(t *testing.T) {
		bad := automaticTVTestTask()
		bad["hashString"] = strings.Repeat("b", 40)
		delete(bad, "labels")
		if out, err := automaticTVTestReader(t, automaticTVTestBody(automaticTVTestTask(), bad)).ListAutomaticTVTasks(context.Background(), automaticTVTestTag); out != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(out, err)
		}
	})
	t.Run("duplicate-task", func(t *testing.T) {
		if out, err := automaticTVTestReader(t, automaticTVTestBody(automaticTVTestTask(), automaticTVTestTask())).ListAutomaticTVTasks(context.Background(), automaticTVTestTag); out != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(out, err)
		}
	})
	for _, labels := range [][]string{{}, {"x" + automaticTVTestTag}, {automaticTVTestTag + "suffix"}, {"other," + automaticTVTestTag}} {
		t.Run(fmt.Sprint(labels), func(t *testing.T) {
			task := automaticTVTestTask()
			task["labels"] = labels
			out, err := automaticTVTestReader(t, automaticTVTestBody(task)).ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
			if err != nil || out == nil || len(out) != 0 {
				t.Fatal(out, err)
			}
		})
	}
}

func TestAutomaticTVExactCompletionAndStates(t *testing.T) {
	for status := 0; status <= 6; status++ {
		for _, nativeError := range []int{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("status%d-error%d", status, nativeError), func(t *testing.T) {
				task := automaticTVTestTask()
				task["status"] = status
				task["error"] = nativeError
				out, err := automaticTVTestReader(t, automaticTVTestBody(task)).ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
				state := []string{"stopped", "checking", "checking", "queued", "downloading", "queued", "seeding"}[status]
				if nativeError != 0 {
					state = "error"
				}
				want := nativeError == 0 && (status == 0 || status == 5 || status == 6)
				if err != nil || len(out) != 1 || out[0].State != state || out[0].Complete != want {
					t.Fatal(out, err)
				}
			})
		}
	}
	for _, size := range []int64{0, 1<<53 + 1, math.MaxInt64} {
		for _, delta := range []int64{0, 1} {
			if size < delta {
				continue
			}
			t.Run(fmt.Sprintf("size%d-minus%d", size, delta), func(t *testing.T) {
				task := automaticTVTestTask()
				task["totalSize"] = size
				task["haveValid"] = size - delta
				task["percentDone"] = 1
				out, err := automaticTVTestReader(t, automaticTVTestBody(task)).ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
				if err != nil || len(out) != 1 || out[0].TotalBytes != size || out[0].VerifiedBytes != size-delta || out[0].Complete != (size > 0 && delta == 0) {
					t.Fatal(out, err)
				}
			})
		}
	}
}

func TestAutomaticTVResponseEnvelope(t *testing.T) {
	valid := automaticTVTestBody(automaticTVTestTask())
	bad := []string{"", "null", "{}", `{"result":"success"}`, `{"result":"success","arguments":null}`, `{"result":"success","arguments":{}}`, `{"result":"success","arguments":{"torrents":null}}`, `{"result":"success","arguments":{"torrents":{}}}`, `{"result":"fixture-secret-url-cookie"}`, `{"result":null,"arguments":{"torrents":[]}}`, valid + "{}", valid[:len(valid)-1], strings.Replace(valid, `"result":`, `"result":"failure","result":`, 1), strings.Replace(valid, `"arguments":`, `"extension":{"a":[{"x":1,"x":2}]},"arguments":`, 1), strings.Replace(valid, `"arguments":`, `"extension":{"x":1,"\u0078":2},"arguments":`, 1), strings.Repeat(" ", 1<<20+1), strings.Replace(valid, "/downloads/TV", string([]byte{0xff}), 1)}
	for i, body := range bad {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := automaticTVTestReader(t, body)
			tasks, err := r.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
			if !errors.Is(err, ports.ErrUnavailable) || tasks != nil {
				t.Fatal(tasks, err)
			}
			files, err := r.GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
			if !errors.Is(err, ports.ErrUnavailable) || files != nil {
				t.Fatal(files, err)
			}
			if strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("sensitive body exposed")
			}
		})
	}
	t.Run("proven-empty", func(t *testing.T) {
		r := automaticTVTestReader(t, automaticTVTestBody())
		out, err := r.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
		if err != nil || out == nil || len(out) != 0 {
			t.Fatal(out, err)
		}
		if files, err := r.GetAutomaticTVFiles(context.Background(), automaticTVTestHash); files != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(files, err)
		}
	})
}

func TestAutomaticTVFilesStrictNativeInventory(t *testing.T) {
	for _, where := range []string{"files", "fileStats"} {
		fields := []string{"bytesCompleted"}
		if where == "files" {
			fields = append(fields, "name", "length")
		} else {
			fields = append(fields, "wanted")
		}
		for _, field := range fields {
			for _, kind := range []string{"missing", "null", "wrong-type", "duplicate"} {
				t.Run(where+"/"+field+"/"+kind, func(t *testing.T) {
					task := automaticTVTestTask()
					entry := task[where].([]any)[1].(map[string]any)
					switch kind {
					case "missing":
						delete(entry, field)
					case "null":
						entry[field] = nil
					case "wrong-type":
						entry[field] = []int{1}
					}
					body := automaticTVTestBody(task)
					if kind == "duplicate" {
						body = strings.Replace(body, `"`+field+`":`, `"`+field+`":null,"`+field+`":`, 1)
					}
					out, err := automaticTVTestReader(t, body).GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
					if out != nil || !errors.Is(err, ports.ErrUnavailable) {
						t.Fatal(out, err)
					}
				})
			}
		}
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"foreign-hash", func(m map[string]any) { m["hashString"] = strings.Repeat("b", 40) }},
		{"missing-files", func(m map[string]any) { delete(m, "files") }}, {"null-files", func(m map[string]any) { m["files"] = nil }}, {"empty-files", func(m map[string]any) { m["files"] = []any{} }},
		{"missing-stats", func(m map[string]any) { delete(m, "fileStats") }}, {"null-stats", func(m map[string]any) { m["fileStats"] = nil }}, {"stats-count", func(m map[string]any) { m["fileStats"] = m["fileStats"].([]any)[:1] }},
		{"sum", func(m map[string]any) { m["totalSize"] = 33 }},
		{"verified-over-completed", func(m map[string]any) {
			m["files"].([]any)[0].(map[string]any)["bytesCompleted"] = 31
			m["fileStats"].([]any)[0].(map[string]any)["bytesCompleted"] = 31
		}},
		{"stats-disagree", func(m map[string]any) { m["fileStats"].([]any)[0].(map[string]any)["bytesCompleted"] = 31 }},
		{"negative-size", func(m map[string]any) { m["files"].([]any)[1].(map[string]any)["length"] = -1 }},
		{"completed-over-size", func(m map[string]any) { m["files"].([]any)[1].(map[string]any)["bytesCompleted"] = 1 }},
		{"negative-completed", func(m map[string]any) { m["files"].([]any)[1].(map[string]any)["bytesCompleted"] = -1 }},
		{"fraction-size", func(m map[string]any) { m["files"].([]any)[1].(map[string]any)["length"] = json.RawMessage("0.0") }},
		{"overflow-size", func(m map[string]any) {
			m["files"].([]any)[1].(map[string]any)["length"] = json.RawMessage("9223372036854775808")
		}},
		{"sum-overflow", func(m map[string]any) {
			m["totalSize"] = int64(math.MaxInt64)
			m["files"].([]any)[0].(map[string]any)["length"] = int64(math.MaxInt64)
			m["files"].([]any)[1].(map[string]any)["length"] = 1
		}},
	}
	for _, p := range []string{"", ".", "..", "../a", "/a", "a/../b", "a//b", "a/", "a\\b", "C:a", "a\x00b", "a\nb", "a\tb", "Show/EPISODE.MKV", "Show/episode.mkv/child", "Show"} {
		p := p
		cases = append(cases, struct {
			name   string
			mutate func(map[string]any)
		}{"path-" + fmt.Sprintf("%q", p), func(m map[string]any) { m["files"].([]any)[1].(map[string]any)["name"] = p }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := automaticTVTestTask()
			tc.mutate(task)
			out, err := automaticTVTestReader(t, automaticTVTestBody(task)).GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
			if out != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(out, err)
			}
		})
	}
	t.Run("multiple", func(t *testing.T) {
		out, err := automaticTVTestReader(t, automaticTVTestBody(automaticTVTestTask(), automaticTVTestTask())).GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
		if out != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(out, err)
		}
	})
	t.Run("partial-exact", func(t *testing.T) {
		task := automaticTVTestTask()
		task["totalSize"] = int64(1<<53 + 1)
		task["haveValid"] = int64(1 << 53)
		task["files"].([]any)[0].(map[string]any)["length"] = int64(1<<53 + 1)
		task["files"].([]any)[0].(map[string]any)["bytesCompleted"] = int64(1 << 53)
		task["fileStats"].([]any)[0].(map[string]any)["bytesCompleted"] = int64(1 << 53)
		out, err := automaticTVTestReader(t, automaticTVTestBody(task)).GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
		if err != nil || len(out) != 2 || out[0].CompletedBytes != 1<<53 || out[0].Size != 1<<53+1 {
			t.Fatal(out, err)
		}
	})
}

func TestAutomaticTVFilesCountAndPathConflicts(t *testing.T) {
	for _, count := range []int{4096, 4097} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			task := automaticTVTestTask()
			files := []any{}
			stats := []any{}
			for i := 0; i < count; i++ {
				files = append(files, map[string]any{"name": fmt.Sprintf("f%d", i), "length": 0, "bytesCompleted": 0})
				stats = append(stats, map[string]any{"bytesCompleted": 0, "wanted": false})
			}
			task["files"] = files
			task["fileStats"] = stats
			task["totalSize"] = 0
			task["haveValid"] = 0
			out, err := automaticTVTestReader(t, automaticTVTestBody(task)).GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
			if count == 4096 {
				if err != nil || len(out) != count {
					t.Fatal(len(out), err)
				}
			} else if out != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(len(out), err)
			}
		})
	}
	for _, names := range [][]string{{"a", "a-b", "a/b"}, {"Σ.mkv", "ς.mkv"}, {"A", "a/b"}} {
		t.Run(strings.Join(names, "-"), func(t *testing.T) {
			task := automaticTVTestTask()
			files := []any{}
			stats := []any{}
			for _, name := range names {
				files = append(files, map[string]any{"name": name, "length": 0, "bytesCompleted": 0})
				stats = append(stats, map[string]any{"bytesCompleted": 0, "wanted": false})
			}
			task["files"] = files
			task["fileStats"] = stats
			task["totalSize"] = 0
			task["haveValid"] = 0
			out, err := automaticTVTestReader(t, automaticTVTestBody(task)).GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
			if out != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(out, err)
			}
		})
	}
}

func TestAutomaticTVHTTPFailuresAndRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	for _, status := range []int{204, 206, 301, 302, 303, 307, 308, 400, 401, 403, 409, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				if status == 206 {
					_, _ = io.WriteString(w, automaticTVTestBody())
				}
			}))
			defer srv.Close()
			c, _ := NewClient(srv.URL, "fixture-user", "fixture-password", srv.Client())
			out, err := c.AutomaticTVObserver().ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
			want := ports.ErrUnavailable
			if status == 401 || status == 403 || status == 409 {
				want = ports.ErrAuthRequired
			}
			if status == 429 {
				if _, ok := ports.IsRateLimited(err); !ok {
					t.Fatal(err)
				}
			} else if !errors.Is(err, want) {
				t.Fatal(out, err)
			}
			if out != nil || calls.Load() != 1 {
				t.Fatal(out, calls.Load())
			}
		})
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect forwarded credentials")
	}
	t.Run("session-loop-bounded", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("X-Transmission-Session-Id", "new")
			w.WriteHeader(409)
		}))
		defer srv.Close()
		c, _ := NewClient(srv.URL, "", "", nil)
		if out, err := c.AutomaticTVObserver().ListAutomaticTVTasks(context.Background(), automaticTVTestTag); out != nil || !errors.Is(err, ports.ErrUnavailable) || calls.Load() != 2 {
			t.Fatal(out, err, calls.Load())
		}
	})
	t.Run("truncated-body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "10000")
			_, _ = io.WriteString(w, automaticTVTestBody())
		}))
		defer srv.Close()
		c, _ := NewClient(srv.URL, "", "", nil)
		out, err := c.AutomaticTVObserver().ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
		if out != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(out, err)
		}
	})
}

func TestAutomaticTVCancellationAndTimeout(t *testing.T) {
	for _, phase := range []string{"before-headers", "during-body"} {
		for _, mode := range []string{"cancel", "deadline", "http-timeout"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if phase == "during-body" {
						w.WriteHeader(200)
						_, _ = io.WriteString(w, `{"result":`)
						w.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}))
				defer srv.Close()
				defer close(release)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := context.Canceled
				client := srv.Client()
				if mode == "deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(context.Background(), 100*time.Millisecond)
					defer stop()
					want = context.DeadlineExceeded
				}
				if mode == "http-timeout" {
					client.Timeout = 100 * time.Millisecond
					want = ports.ErrTimeout
				}
				c, _ := NewClient(srv.URL, "fixture-user", "fixture-password", client)
				done := make(chan error, 1)
				go func() { _, err := c.AutomaticTVObserver().ListAutomaticTVTasks(ctx, automaticTVTestTag); done <- err }()
				<-started
				if mode == "cancel" {
					cancel()
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) || strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "fixture-password") {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("observation did not stop")
				}
			})
		}
	}
	t.Run("already-canceled-input", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var c *Client
		r := c.AutomaticTVObserver()
		if _, err := r.ListAutomaticTVTasks(ctx, "bad"); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := r.GetAutomaticTVFiles(ctx, "bad"); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestAutomaticTVInvalidInputNoNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "", "", nil)
	r := c.AutomaticTVObserver()
	for _, tag := range []string{"", "bad", strings.TrimPrefix(automaticTVTestTag, "pulsemedia:op:"), automaticTVTestTag + " ", "pulsemedia:op:00000000-0000-0000-0000-000000000000"} {
		if out, err := r.ListAutomaticTVTasks(context.Background(), tag); out != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(out, err)
		}
	}
	for _, hash := range []string{"", strings.Repeat("a", 64), strings.Repeat("x", 40), automaticTVTestHash + " "} {
		if out, err := r.GetAutomaticTVFiles(context.Background(), hash); out != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(out, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached network")
	}
}

func TestAutomaticTVConcurrentBoundReaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "session")
			w.WriteHeader(409)
			return
		}
		_, _ = io.WriteString(w, automaticTVTestBody(automaticTVTestTask()))
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "", "", nil)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			r := c.AutomaticTVObserver()
			if _, err := r.ListAutomaticTVTasks(context.Background(), automaticTVTestTag); err != nil {
				t.Error(err)
			}
			if _, err := r.GetAutomaticTVFiles(context.Background(), automaticTVTestHash); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
func TestAutomaticTVAllTasksWithoutImplicitLimit(t *testing.T) {
	const count = 1200
	tasks := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		task := automaticTVTestTask()
		delete(task, "files")
		delete(task, "fileStats")
		task["hashString"] = fmt.Sprintf("%040x", i+1)
		task["labels"] = []string{"unrelated"}
		tasks = append(tasks, task)
	}
	tasks[count-1]["labels"] = []string{automaticTVTestTag}
	out, err := automaticTVTestReader(t, automaticTVTestBody(tasks...)).ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
	if err != nil || len(out) != 1 || out[0].InfoHash != fmt.Sprintf("%040x", count) {
		t.Fatal(out, err)
	}
	delete(tasks[count-2], "haveValid")
	out, err = automaticTVTestReader(t, automaticTVTestBody(tasks...)).ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
	if out != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(out, err)
	}
}

func TestAutomaticTVBodyBoundaryAndJSONDepth(t *testing.T) {
	base := automaticTVTestBody()
	for _, size := range []int{1 << 20, 1<<20 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := base + strings.Repeat(" ", size-len(base))
			out, err := automaticTVTestReader(t, body).ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
			if size == 1<<20 {
				if err != nil || out == nil || len(out) != 0 {
					t.Fatal(out, err)
				}
			} else if out != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(out, err)
			}
		})
	}
	deep := strings.Replace(base, `"result":`, `"extension":`+strings.Repeat("[", 65)+"0"+strings.Repeat("]", 65)+`,"result":`, 1)
	if out, err := automaticTVTestReader(t, deep).ListAutomaticTVTasks(context.Background(), automaticTVTestTag); out != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(out, err)
	}
}
