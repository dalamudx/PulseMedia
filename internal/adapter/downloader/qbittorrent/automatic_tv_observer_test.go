package qbittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const automaticTVTestHash = "abcdef0123456789abcdef0123456789abcdef01"
const automaticTVTestTag = "pulsemedia:op:01985f54-3d21-7000-8000-000000000001"

func automaticTVTestTask() map[string]any {
	return map[string]any{"hash": automaticTVTestHash, "save_path": "/downloads/tv/", "content_path": "/downloads/tv/episode.mkv", "tags": "other, " + automaticTVTestTag + " , last", "state": "stoppedUP", "total_size": int64(5), "completed": int64(5), "progress": 1, "name": "episode", "size": 4, "downloaded": 999999}
}

func automaticTVTestFiles() []map[string]any {
	return []map[string]any{
		{"index": 0, "name": "episode.mkv", "size": int64(4), "priority": 1, "progress": 1},
		{"index": 1, "name": "unwanted.nfo", "size": int64(1), "priority": 0, "progress": 1},
		{"index": 2, "name": "empty.txt", "size": int64(0), "priority": 6, "progress": 1},
	}
}

func automaticTVJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func automaticTVTaskBody(t *testing.T, row map[string]any) string {
	t.Helper()
	return automaticTVJSON(t, []map[string]any{row})
}

type automaticTVRequest struct {
	method, endpoint string
	query            url.Values
}

type automaticTVNativeFixture struct {
	mu       sync.Mutex
	requests []automaticTVRequest
}

func (f *automaticTVNativeFixture) snapshot() []automaticTVRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]automaticTVRequest(nil), f.requests...)
}

// Exercise NewClient, cookie authentication and the real HTTP transport for all
// payload cases, including the prefix and exact query sent to native qB.
func automaticTVNative(t *testing.T, infoBodies []string, fileBody string) (ports.AutomaticTVReadClient, *automaticTVNativeFixture) {
	t.Helper()
	f := &automaticTVNativeFixture{}
	infoCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := strings.TrimPrefix(r.URL.Path, "/qb/prefix/api/v2/")
		if r.URL.EscapedPath() != "/qb%2Fprefix/api/v2/"+endpoint {
			t.Error("configured native path encoding changed")
		}
		f.mu.Lock()
		f.requests = append(f.requests, automaticTVRequest{r.Method, endpoint, r.URL.Query()})
		f.mu.Unlock()
		if endpoint == "auth/login" {
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("username") != "fixture-user" || r.Form.Get("password") != "fixture-secret" {
				t.Error("invalid native authentication")
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "fixture-session", Path: "/qb/prefix"})
			_, _ = io.WriteString(w, "Ok.")
			return
		}
		cookie, err := r.Cookie("SID")
		if err != nil || cookie.Value != "fixture-session" || r.Method != http.MethodGet {
			t.Error("native read missing authentication or wrong method")
		}
		switch endpoint {
		case "torrents/info":
			f.mu.Lock()
			i := min(infoCalls, len(infoBodies)-1)
			infoCalls++
			f.mu.Unlock()
			_, _ = io.WriteString(w, infoBodies[i])
		case "torrents/files":
			_, _ = io.WriteString(w, fileBody)
		default:
			t.Errorf("unexpected native endpoint: %s", endpoint)
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(server.Close)
	borrowed := server.Client()
	redirect := func(*http.Request, []*http.Request) error { return errors.New("borrowed redirect policy") }
	borrowed.CheckRedirect = redirect
	client, err := NewClient(server.URL+"/qb%2Fprefix", "fixture-user", "fixture-secret", borrowed)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(borrowed.CheckRedirect).Pointer() != reflect.ValueOf(redirect).Pointer() {
		t.Fatal("borrowed HTTP client mutated")
	}
	return client.AutomaticTVObserver(), f
}

func TestAutomaticTVNativeCapabilityAndExactRequests(t *testing.T) {
	reader, fixture := automaticTVNative(t, []string{automaticTVTaskBody(t, automaticTVTestTask())}, automaticTVJSON(t, automaticTVTestFiles()))
	kind := reflect.TypeOf(reader)
	if kind.NumMethod() != 2 {
		t.Fatalf("read capability exposes %d methods", kind.NumMethod())
	}
	for _, name := range []string{"ListAutomaticTVTasks", "GetAutomaticTVFiles"} {
		if _, ok := kind.MethodByName(name); !ok {
			t.Fatalf("missing read method %s", name)
		}
	}
	tasks, err := reader.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
	if err != nil || len(tasks) != 1 || !tasks[0].Complete || tasks[0].VerifiedBytes != 5 || tasks[0].TotalBytes != 5 || tasks[0].InfoHash != automaticTVTestHash || !reflect.DeepEqual(tasks[0].Tags, []string{"other", automaticTVTestTag, "last"}) {
		t.Fatalf("unexpected native tasks: %+v, %v", tasks, err)
	}
	files, err := reader.GetAutomaticTVFiles(context.Background(), strings.ToUpper(automaticTVTestHash))
	want := []ports.AutomaticTVRemoteFile{{Index: 0, Path: "episode.mkv", Size: 4, CompletedBytes: 4, Wanted: true}, {Index: 1, Path: "unwanted.nfo", Size: 1, CompletedBytes: 1}, {Index: 2, Path: "empty.txt", Wanted: true}}
	if err != nil || !reflect.DeepEqual(files, want) {
		t.Fatalf("full inventory: %+v, %v", files, err)
	}
	got := fixture.snapshot()
	expected := []automaticTVRequest{
		{http.MethodPost, "auth/login", url.Values{}},
		{http.MethodGet, "torrents/info", url.Values{"tag": {automaticTVTestTag}}},
		{http.MethodGet, "torrents/info", url.Values{"hashes": {automaticTVTestHash}}},
		{http.MethodGet, "torrents/files", url.Values{"hash": {automaticTVTestHash}}},
		{http.MethodGet, "torrents/info", url.Values{"hashes": {automaticTVTestHash}}},
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("native request sequence: %+v", got)
	}
}

func TestAutomaticTVTaskPayloads(t *testing.T) {
	base := automaticTVTaskBody(t, automaticTVTestTask())
	cases := map[string]string{"null": "null", "object": "{}", "truncated": base[:len(base)-1], "trailing": base + "[]", "null-row": "[null]", "empty-object": "[{}]", "nested-duplicate": strings.Replace(base, `"state":`, `"unused":{"a":1,"A":2},"state":`, 1), "duplicate-field": strings.Replace(base, `"hash":`, `"HASH":"`+automaticTVTestHash+`","hash":`, 1), "unicode-alias": strings.Replace(base, `"state":`, `"ſtate":"uploading","state":`, 1), "escaped-duplicate": strings.Replace(base, `"state":`, `"st\u0061te":"uploading","state":`, 1), "oversize": base + strings.Repeat(" ", (1<<20)+1)}
	for _, field := range []string{"hash", "save_path", "tags", "state", "total_size", "completed"} {
		for _, mode := range []string{"missing", "null", "wrong-type"} {
			row := automaticTVTestTask()
			switch mode {
			case "missing":
				delete(row, field)
			case "null":
				row[field] = nil
			default:
				row[field] = []int{1}
			}
			cases[field+"/"+mode] = automaticTVTaskBody(t, row)
		}
	}
	invalid := map[string][]any{
		"hash":         {"all", strings.Repeat("a", 64), strings.Repeat("g", 40), automaticTVTestHash + "|" + automaticTVTestHash},
		"save_path":    {"", "relative", "/downloads/../escape", "/downloads/./tv", "/downloads//tv", "/downloads\t/tv", "//", "C://", "C:relative", `C:\downloads\..\escape`, `C:\downloads:stream`, "/downloads\u007f"},
		"tags":         {"", "other", automaticTVTestTag + "suffix", "prefix" + automaticTVTestTag, automaticTVTestTag + "\r\nother", automaticTVTestTag + "\t"},
		"state":        {"", "unknown", "new-state", "UPLOADING", "uploading\n"},
		"total_size":   {int64(-1), 1.5, json.Number("9223372036854775808"), "5"},
		"completed":    {int64(-1), 1.5, json.Number("9223372036854775808"), int64(6), "5"},
		"progress":     {nil, "1", -0.01, 1.1, json.Number("1.00000000000000000000000000000001"), json.Number("-1e-999")},
		"name":         {nil, 1, "", "episode\x00"},
		"content_path": {nil, 1, "", "relative", "/downloads/../episode.mkv"},
	}
	for field, values := range invalid {
		for i, value := range values {
			row := automaticTVTestTask()
			row[field] = value
			cases[fmt.Sprintf("%s/invalid-%d", field, i)] = automaticTVTaskBody(t, row)
		}
	}
	row := automaticTVTestTask()
	row["hash"] = strings.ToUpper(automaticTVTestHash)
	cases["duplicate-hash-case"] = automaticTVJSON(t, []map[string]any{automaticTVTestTask(), row})
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := automaticTVNative(t, []string{body}, "[]")
			got, err := reader.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
			if !errors.Is(err, ports.ErrUnavailable) || got != nil {
				t.Fatalf("untrusted response accepted: %+v %v", got, err)
			}
		})
	}
}

func TestAutomaticTVCompleteEmptyAndTaskBounds(t *testing.T) {
	for _, count := range []int{0, 64, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			rows := make([]map[string]any, 0, count)
			for i := range count {
				row := automaticTVTestTask()
				row["hash"] = fmt.Sprintf("%040x", i+1)
				rows = append(rows, row)
			}
			reader, _ := automaticTVNative(t, []string{automaticTVJSON(t, rows)}, "[]")
			got, err := reader.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
			if count <= 64 {
				if err != nil || got == nil || len(got) != count {
					t.Fatalf("complete lookup: %d %v", len(got), err)
				}
			} else if !errors.Is(err, ports.ErrUnavailable) || got != nil {
				t.Fatal("task bound bypassed", err)
			}
		})
	}
}

func TestAutomaticTVNativeIntegerCompletionAndStates(t *testing.T) {
	for _, total := range []int64{1_000_000, 1<<53 + 1, math.MaxInt64} {
		for _, delta := range []int64{0, 1} {
			t.Run(fmt.Sprintf("%d/minus-%d", total, delta), func(t *testing.T) {
				row := automaticTVTestTask()
				row["total_size"], row["completed"] = total, total-delta
				reader, _ := automaticTVNative(t, []string{automaticTVTaskBody(t, row)}, "[]")
				got, err := reader.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
				if err != nil || len(got) != 1 || got[0].Complete != (delta == 0) || got[0].VerifiedBytes != total-delta {
					t.Fatalf("native integer proof lost: %+v %v", got, err)
				}
				if delta == 1 {
					files, err := reader.GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
					if !errors.Is(err, ports.ErrUnavailable) || files != nil {
						t.Fatal("incomplete integer promoted to file proof", err)
					}
				}
			})
		}
	}
	for _, state := range []string{"uploading", "stalledUP", "pausedUP", "stoppedUP", "queuedUP", "forcedUP", "error", "missingFiles", "checkingUP", "allocating", "downloading", "metaDL", "forcedMetaDL", "pausedDL", "stoppedDL", "queuedDL", "stalledDL", "checkingDL", "forcedDL", "checkingResumeData", "moving"} {
		t.Run(state, func(t *testing.T) {
			row := automaticTVTestTask()
			row["state"], row["progress"] = state, 0
			reader, _ := automaticTVNative(t, []string{automaticTVTaskBody(t, row)}, "[]")
			got, err := reader.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
			want := state == "uploading" || state == "stalledUP" || state == "pausedUP" || state == "stoppedUP" || state == "queuedUP" || state == "forcedUP"
			if err != nil || len(got) != 1 || got[0].Complete != want {
				t.Fatalf("state completion incorrect: %+v %v", got, err)
			}
		})
	}
	for _, savePath := range []string{"/", "/downloads/", `C:\downloads\tv\`, "D:/downloads/tv", `\\server\share\tv`} {
		t.Run(savePath, func(t *testing.T) {
			row := automaticTVTestTask()
			row["save_path"], row["hash"] = savePath, strings.ToUpper(automaticTVTestHash)
			delete(row, "progress")
			delete(row, "name")
			delete(row, "content_path")
			reader, _ := automaticTVNative(t, []string{automaticTVTaskBody(t, row)}, "[]")
			got, err := reader.ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
			if err != nil || len(got) != 1 || got[0].InfoHash != automaticTVTestHash || got[0].SavePath != savePath {
				t.Fatalf("valid native identity rejected: %+v %v", got, err)
			}
		})
	}
}

func TestAutomaticTVFilesRejectIncompleteOrInvalidInventory(t *testing.T) {
	base := automaticTVJSON(t, automaticTVTestFiles())
	cases := map[string]string{"null": "null", "empty": "[]", "null-row": "[null]", "truncated": base[:len(base)-1], "trailing": base + "[]", "duplicate": strings.Replace(base, `"index":`, `"INDEX":0,"index":`, 1), "nested-duplicate": strings.Replace(base, `"index":`, `"unused":[{"a":1,"a":2}],"index":`, 1), "oversize": base + strings.Repeat(" ", (1<<20)+1)}
	for _, field := range []string{"index", "name", "size", "priority", "progress"} {
		for _, mode := range []string{"missing", "null", "wrong-type"} {
			rows := automaticTVTestFiles()
			switch mode {
			case "missing":
				delete(rows[0], field)
			case "null":
				rows[0][field] = nil
			default:
				rows[0][field] = []int{1}
			}
			cases[field+"/"+mode] = automaticTVJSON(t, rows)
		}
	}
	invalid := map[string][]any{
		"index":    {-1, 1, 3, 0.5, json.Number("9223372036854775808")},
		"name":     {"", ".", "..", "../escape", "/abs", "a/../b", "a/./b", "a//b", "a/", "C:file", `a\b`, "a\x01b", "EMPTY.TXT"},
		"size":     {-1, 1.5, json.Number("9223372036854775808"), 3, 5},
		"priority": {-1, 1.5, json.Number("9223372036854775808"), "1"},
		"progress": {0, -0.1, 1.1, "1", json.Number("0.99999999999999999999999999999999999999"), json.Number("1.00000000000000000000000000000001"), json.Number("-1e-999")},
	}
	for field, values := range invalid {
		for i, value := range values {
			rows := automaticTVTestFiles()
			rows[0][field] = value
			cases[fmt.Sprintf("%s/invalid-%d", field, i)] = automaticTVJSON(t, rows)
		}
	}
	rows := automaticTVTestFiles()
	rows[1]["index"] = 0
	cases["duplicate-index"] = automaticTVJSON(t, rows)
	rows = automaticTVTestFiles()
	rows[1]["size"] = int64(math.MaxInt64)
	cases["sum-overflow"] = automaticTVJSON(t, rows)
	cases["omitted-unwanted"] = automaticTVJSON(t, automaticTVTestFiles()[:1])
	rows = append(automaticTVTestFiles(), map[string]any{"index": 3, "name": "extra.mkv", "size": 1, "priority": 1, "progress": 1})
	cases["extra-total"] = automaticTVJSON(t, rows)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := automaticTVNative(t, []string{automaticTVTaskBody(t, automaticTVTestTask())}, body)
			got, err := reader.GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
			if !errors.Is(err, ports.ErrUnavailable) || got != nil {
				t.Fatalf("unproven files accepted: %+v %v", got, err)
			}
		})
	}
}

func TestAutomaticTVFilesRequireStableWholeTorrentProof(t *testing.T) {
	base := automaticTVTaskBody(t, automaticTVTestTask())
	cases := map[string]string{"empty": "[]", "null": "null", "duplicate-task": automaticTVJSON(t, []map[string]any{automaticTVTestTask(), automaticTVTestTask()})}
	for field, value := range map[string]any{"hash": strings.Repeat("b", 40), "save_path": "/other", "content_path": "/downloads/other.mkv", "state": "checkingUP", "total_size": int64(6), "completed": int64(4), "tags": nil} {
		row := automaticTVTestTask()
		row[field] = value
		if field == "total_size" {
			row["completed"] = value
		}
		cases[field] = automaticTVTaskBody(t, row)
	}
	for name, changed := range cases {
		for _, phase := range []string{"before", "after"} {
			t.Run(name+"/"+phase, func(t *testing.T) {
				bodies := []string{base, changed}
				if phase == "before" {
					bodies = []string{changed, base}
				}
				reader, _ := automaticTVNative(t, bodies, automaticTVJSON(t, automaticTVTestFiles()))
				got, err := reader.GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
				if !errors.Is(err, ports.ErrUnavailable) || got != nil {
					t.Fatalf("unstable/incomplete task accepted: %+v %v", got, err)
				}
			})
		}
	}
}

func TestAutomaticTVFilesExactLargeIntegersAndBounds(t *testing.T) {
	for _, total := range []int64{0, 1_000_000, 1<<53 + 1, math.MaxInt64} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			row := automaticTVTestTask()
			row["total_size"], row["completed"] = total, total
			files := automaticTVTestFiles()[:1]
			files[0]["size"], files[0]["progress"] = total, json.Number("1.00000000000000000000000000000000000000")
			reader, _ := automaticTVNative(t, []string{automaticTVTaskBody(t, row)}, automaticTVJSON(t, files))
			got, err := reader.GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
			if err != nil || len(got) != 1 || got[0].CompletedBytes != total {
				t.Fatalf("exact integer files: %+v %v", got, err)
			}
		})
	}
	for _, count := range []int{4096, 4097} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			row := automaticTVTestTask()
			row["total_size"], row["completed"] = 0, 0
			files := make([]map[string]any, count)
			for i := range count {
				files[i] = map[string]any{"index": count - i - 1, "name": fmt.Sprintf("%04d.txt", i), "size": 0, "priority": 0, "progress": 1}
			}
			reader, _ := automaticTVNative(t, []string{automaticTVTaskBody(t, row)}, automaticTVJSON(t, files))
			got, err := reader.GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
			if count == 4096 {
				if err != nil || len(got) != count {
					t.Fatal("full bounded file set rejected", err)
				}
			} else if !errors.Is(err, ports.ErrUnavailable) || got != nil {
				t.Fatal("file bound bypassed", err)
			}
		})
	}
}

func TestAutomaticTVInvalidInputsNeverAuthenticate(t *testing.T) {
	reader, fixture := automaticTVNative(t, []string{"[]"}, "[]")
	for _, tag := range []string{"", "pulsemedia", "pulsemedia:op", automaticTVTestTag + ",other", automaticTVTestTag + "&limit=1", automaticTVTestTag + "\r\n", " " + automaticTVTestTag, "pulsemedia:op:00000000-0000-0000-0000-000000000000", strings.ToUpper(automaticTVTestTag)} {
		got, err := reader.ListAutomaticTVTasks(context.Background(), tag)
		if !errors.Is(err, ports.ErrBadResponse) || got != nil {
			t.Fatal("invalid operation label accepted", err)
		}
	}
	for _, hash := range []string{"", "all", strings.Repeat("a", 64), strings.Repeat("g", 40), automaticTVTestHash + "|" + automaticTVTestHash, automaticTVTestHash + "&hash=other"} {
		got, err := reader.GetAutomaticTVFiles(context.Background(), hash)
		if !errors.Is(err, ports.ErrBadResponse) || got != nil {
			t.Fatal("invalid hash accepted", err)
		}
	}
	if len(fixture.snapshot()) != 0 {
		t.Fatal("invalid input reached native HTTP")
	}
}

func TestAutomaticTVHTTPFailuresAndRedirects(t *testing.T) {
	for _, phase := range []string{"list", "before", "files", "after", "login"} {
		for _, status := range []int{302, 401, 403, 404, 429, 500} {
			t.Run(fmt.Sprintf("%s/%d", phase, status), func(t *testing.T) {
				var mu sync.Mutex
				infoCalls, redirects := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.URL.Path == "/leaked-secret" {
						redirects++
					}
					isInfo := r.URL.Path == "/api/v2/torrents/info"
					if isInfo {
						infoCalls++
					}
					fail := (phase == "login" && strings.HasSuffix(r.URL.Path, "/login")) || ((phase == "list" || phase == "before") && isInfo) || (phase == "after" && isInfo && infoCalls == 2) || (phase == "files" && strings.HasSuffix(r.URL.Path, "/files"))
					if fail {
						w.Header().Set("Location", "/leaked-secret")
						w.Header().Set("Retry-After", "3")
						w.WriteHeader(status)
						_, _ = io.WriteString(w, "fixture-secret raw response")
						return
					}
					switch r.URL.Path {
					case "/api/v2/auth/login":
						_, _ = io.WriteString(w, "Ok.")
					case "/api/v2/torrents/info":
						_, _ = io.WriteString(w, automaticTVTaskBody(t, automaticTVTestTask()))
					case "/api/v2/torrents/files":
						_, _ = io.WriteString(w, automaticTVJSON(t, automaticTVTestFiles()))
					default:
						t.Error("unexpected endpoint", r.URL.Path)
					}
				}))
				t.Cleanup(server.Close)
				c, err := NewClient(server.URL, "fixture", "fixture-secret", server.Client())
				if err != nil {
					t.Fatal(err)
				}
				if phase == "list" || phase == "login" {
					_, err = c.AutomaticTVObserver().ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
				} else {
					_, err = c.AutomaticTVObserver().GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
				}
				if err == nil || strings.Contains(err.Error(), "fixture") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "leaked") {
					t.Fatal("unsafe or missing native error", err)
				}
				if status == 401 || status == 403 {
					if !errors.Is(err, ports.ErrAuthRequired) {
						t.Fatal("lost auth classification", err)
					}
				} else if status == 429 {
					if _, ok := ports.IsRateLimited(err); !ok {
						t.Fatal("lost rate classification", err)
					}
				} else if phase != "login" && !errors.Is(err, ports.ErrUnavailable) {
					t.Fatal("lost unavailable classification", err)
				}
				mu.Lock()
				defer mu.Unlock()
				if redirects != 0 {
					t.Fatal("native redirect followed")
				}
			})
		}
	}
}

func TestAutomaticTVRequiresEntireHTTPBody(t *testing.T) {
	for _, phase := range []string{"list", "files", "after"} {
		for _, exactLimit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exact-limit-%t", phase, exactLimit), func(t *testing.T) {
				var infoCalls atomic.Int32
				taskBody := automaticTVTaskBody(t, automaticTVTestTask())
				fileBody := automaticTVJSON(t, automaticTVTestFiles())
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/login") {
						_, _ = io.WriteString(w, "Ok.")
						return
					}
					body := fileBody
					isInfo := strings.HasSuffix(r.URL.Path, "/info")
					if isInfo {
						infoCalls.Add(1)
						body = taskBody
					}
					selected := (phase == "list" && isInfo) || (phase == "files" && !isInfo) || (phase == "after" && isInfo && infoCalls.Load() == 2)
					if selected {
						if phase == "list" {
							body = "[]"
						}
						if exactLimit {
							body += strings.Repeat(" ", int(automaticTVResponseLimit)-len(body))
						} else {
							// A syntactically complete JSON prefix is not a complete HTTP result.
							w.Header().Set("Content-Length", fmt.Sprint(len(body)+20))
						}
					}
					_, _ = io.WriteString(w, body)
				}))
				defer server.Close()
				c, err := NewClient(server.URL, "fixture", "fixture", server.Client())
				if err != nil {
					t.Fatal(err)
				}
				if phase == "list" {
					var got []ports.AutomaticTVRemoteTask
					got, err = c.AutomaticTVObserver().ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
					if exactLimit && (got == nil || len(got) != 0) {
						t.Fatal("complete empty result lost")
					}
				} else {
					_, err = c.AutomaticTVObserver().GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
				}
				if exactLimit && err != nil {
					t.Fatal("exact body bound rejected", err)
				}
				if !exactLimit && !errors.Is(err, ports.ErrUnavailable) {
					t.Fatal("incomplete HTTP body accepted", err)
				}
			})
		}
	}
}

func TestAutomaticTVTransportAndBodyErrors(t *testing.T) {
	for _, operation := range []string{"list", "files"} {
		for _, readFailure := range []bool{false, true} {
			for i, failure := range []error{errors.New("fixture-secret https://credential.invalid/token"), contractTimeout{}, context.Canceled, context.DeadlineExceeded} {
				t.Run(fmt.Sprintf("%s/body-%t/%d", operation, readFailure, i), func(t *testing.T) {
					body := &checkedBody{Reader: brokenHTTPBody{failure}}
					c, err := NewClient("https://credential.invalid", "fixture", "fixture-secret", &http.Client{Transport: contractTransport(func(*http.Request) (*http.Response, error) {
						if !readFailure {
							return nil, failure
						}
						return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
					})})
					if err != nil {
						t.Fatal(err)
					}
					c.loggedIn = true
					if operation == "list" {
						_, err = c.AutomaticTVObserver().ListAutomaticTVTasks(context.Background(), automaticTVTestTag)
					} else {
						_, err = c.AutomaticTVObserver().GetAutomaticTVFiles(context.Background(), automaticTVTestHash)
					}
					want := []error{ports.ErrUnavailable, ports.ErrTimeout, context.Canceled, ports.ErrTimeout}[i]
					if !errors.Is(err, want) || strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "fixture") || (readFailure && !body.closed) {
						t.Fatalf("unsafe transport/body failure: %v, closed=%t", err, body.closed)
					}
				})
			}
		}
	}
}

func TestAutomaticTVCancellationAndDeadline(t *testing.T) {
	for _, phase := range []string{"login", "list", "before", "files", "after"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline-%t", phase, deadline), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var infoCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					isInfo := strings.HasSuffix(r.URL.Path, "/info")
					if isInfo {
						infoCalls.Add(1)
					}
					block := (phase == "login" && strings.HasSuffix(r.URL.Path, "/login")) || ((phase == "list" || phase == "before") && isInfo) || (phase == "files" && strings.HasSuffix(r.URL.Path, "/files")) || (phase == "after" && isInfo && infoCalls.Load() == 2)
					if block {
						close(entered)
						select {
						case <-r.Context().Done():
						case <-release:
						}
						return
					}
					switch {
					case strings.HasSuffix(r.URL.Path, "/login"):
						_, _ = io.WriteString(w, "Ok.")
					case isInfo:
						_, _ = io.WriteString(w, automaticTVTaskBody(t, automaticTVTestTask()))
					default:
						_, _ = io.WriteString(w, automaticTVJSON(t, automaticTVTestFiles()))
					}
				}))
				defer server.Close()
				defer close(release)
				c, err := NewClient(server.URL, "fixture", "fixture", server.Client())
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
				}
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if phase == "login" || phase == "list" {
						_, e := c.AutomaticTVObserver().ListAutomaticTVTasks(ctx, automaticTVTestTag)
						done <- e
					} else {
						_, e := c.AutomaticTVObserver().GetAutomaticTVFiles(ctx, automaticTVTestHash)
						done <- e
					}
				}()
				select {
				case <-entered:
				case err := <-done:
					t.Fatal("request did not reach intended phase", err)
				}
				if !deadline {
					cancel()
				}
				err = <-done
				want := context.Canceled
				if deadline {
					want = ports.ErrTimeout
				}
				if !errors.Is(err, want) {
					t.Fatalf("context error %v want %v", err, want)
				}
			})
		}
	}
}
