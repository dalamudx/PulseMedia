package shooter

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const chineseSRT = "1\n00:00:01,000 --> 00:00:04,000\n你好，世界！\n\n"

func videoHash() ports.SubtitleVideoHash {
	return ports.SubtitleVideoHash{Hash: strings.Repeat(strings.Repeat("a", 32)+";", 3) + strings.Repeat("b", 32), SizeBytes: 32768}
}

func packageJSON(ext, link string) string {
	b, _ := json.Marshal([]any{map[string]any{"Desc": "中文", "Delay": 0, "extra": true, "Files": []any{map[string]any{"Ext": ext, "Link": link}}}})
	return string(b)
}

func TestFindChineseSubtitleProtocol(t *testing.T) {
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(chineseSRT))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ext string
		body      []byte
		gzip      bool
	}{
		{"utf8", "srt", []byte(chineseSRT), false},
		{"gb18030", ".SrT", gb, false},
		{"transparent_gzip", "SRT", []byte(chineseSRT), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			const signedQuery = "fetch=a%2Fb%2Bc&nonce=secret%3D&x=1&x=2"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.RawQuery == signedQuery {
					if r.Method != http.MethodGet {
						t.Errorf("download method = %s", r.Method)
					}
					if tc.gzip {
						w.Header().Set("Content-Encoding", "gzip")
						zw := gzip.NewWriter(w)
						_, e := zw.Write(tc.body)
						if e != nil {
							t.Error(e)
						}
						if e := zw.Close(); e != nil {
							t.Error(e)
						}
					} else {
						_, _ = w.Write(tc.body)
					}
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/subapi.php" {
					t.Errorf("unexpected lookup method/path")
				}
				if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
					t.Error("not standard form")
				}
				if e := r.ParseForm(); e != nil {
					t.Error(e)
				}
				want := url.Values{"filehash": {videoHash().Hash}, "pathinfo": {""}, "format": {"json"}, "lang": {"chn"}}
				if !reflect.DeepEqual(r.PostForm, want) {
					t.Errorf("form = %#v", r.PostForm)
				}
				_, _ = io.WriteString(w, packageJSON(tc.ext, "http://"+r.Host+"/api/subapi.php?"+signedQuery))
			}))
			defer server.Close()
			client := NewClient(server.URL+"/api/subapi.php", server.Client())
			got, err := client.FindChineseSubtitle(t.Context(), videoHash())
			if err != nil || string(got) != chineseSRT {
				t.Fatalf("result %q, %v", got, err)
			}
			got[0] = '9'
			again, err := client.FindChineseSubtitle(t.Context(), videoHash())
			if err != nil || string(again) != chineseSRT {
				t.Fatalf("result not independently owned: %v", err)
			}
			if requests.Load() != 4 {
				t.Fatalf("requests = %d", requests.Load())
			}
		})
	}
}

func TestLookupResponses(t *testing.T) {
	cases := []struct {
		name, body string
		empty      bool
	}{
		{"no_results_byte", string([]byte{0xff}), true},
		{"empty_array", "[]", true},
		{"empty_files", `[{"Files":[]}]`, true},
		{"null", "null", false},
		{"object", "{}", false},
		{"empty_body", "", false},
		{"html", "<html>secret</html>", false},
		{"invalid_json", "[", false},
		{"trailing_json", "[] []", false},
		{"null_package", "[null]", false},
		{"missing_files", "[{}]", false},
		{"null_files", `[{"Files":null}]`, false},
		{"wrong_files_type", `[{"Files":{}}]`, false},
		{"null_file", `[{"Files":[null]}]`, false},
		{"missing_fields", `[{"Files":[{}]}]`, false},
		{"null_ext", `[{"Files":[{"Ext":null,"Link":"@ORIGIN@/sub"}]}]`, false},
		{"number_ext", `[{"Files":[{"Ext":1,"Link":"@ORIGIN@/sub"}]}]`, false},
		{"number_link", `[{"Files":[{"Ext":"srt","Link":1}]}]`, false},
		{"null_link", `[{"Files":[{"Ext":"srt","Link":null}]}]`, false},
		{"empty_link", `[{"Files":[{"Ext":"srt","Link":""}]}]`, false},
		{"empty_ext", `[{"Files":[{"Ext":"","Link":"@ORIGIN@/sub"}]}]`, false},
		{"ass_only", packageJSON("ass", "@ORIGIN@/sub"), true},
		{"not_exact_srt", packageJSON("..srt", "@ORIGIN@/sub"), true},
		{"whitespace_ext", packageJSON(" srt ", "@ORIGIN@/sub"), true},
		{"api_oversize", strings.Repeat(" ", maxAPIBytes) + "[]", false},
		{"too_many_packages", "[" + strings.Repeat(`{"Files":[]},`, maxPackages) + `{"Files":[]}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				// Flushing also exercises the read limit without Content-Length.
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, strings.ReplaceAll(tc.body, "@ORIGIN@", "http://"+r.Host))
			}))
			defer s.Close()
			got, err := NewClient(s.URL, nil).FindChineseSubtitle(t.Context(), videoHash())
			if got != nil {
				t.Fatal("unexpected result")
			}
			if tc.empty && err != nil {
				t.Fatal(err)
			}
			if !tc.empty && !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("error = %v", err)
			}
			if requests.Load() != 1 {
				t.Fatal("unexpected download")
			}
		})
	}
}

func TestRejectUnsafeLinks(t *testing.T) {
	var remoteRequests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { remoteRequests.Add(1) }))
	defer remote.Close()
	for _, link := range []string{remote.URL + "/secret", "https://example.invalid/secret", "https://@HOST@/secret", "http://user:pass@@HOST@/secret", "http://@HOST@/secret#fragment", "http://@HOST@/secret#", "/relative", "//example.invalid/sub", "file:///secret", "http://@HOST@/%zz"} {
		t.Run(link, func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, packageJSON("srt", strings.ReplaceAll(link, "@HOST@", r.Host)))
			}))
			defer s.Close()
			got, err := NewClient(s.URL, nil).FindChineseSubtitle(t.Context(), videoHash())
			if got != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("result = %q, %v", got, err)
			}
			if requests.Load() != 1 || remoteRequests.Load() != 0 {
				t.Fatal("unsafe link requested")
			}
		})
	}
}

func TestNoRedirects(t *testing.T) {
	for _, stage := range []string{"lookup", "download"} {
		for _, external := range []bool{false, true} {
			t.Run(stage+"/external="+strconvBool(external), func(t *testing.T) {
				var redirected atomic.Int32
				other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
				defer other.Close()
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/target" {
						redirected.Add(1)
						return
					}
					if stage == "download" && r.Method == http.MethodPost {
						_, _ = io.WriteString(w, packageJSON("srt", "http://"+r.Host+"/download"))
						return
					}
					target := "/target"
					if external {
						target = other.URL + "/target"
					}
					http.Redirect(w, r, target, http.StatusFound)
				}))
				defer s.Close()
				original := s.Client()
				var callback atomic.Int32
				original.CheckRedirect = func(*http.Request, []*http.Request) error { callback.Add(1); return nil }
				jar, _ := cookiejar.New(nil)
				original.Jar = jar
				client := NewClient(s.URL, original)
				_, err := client.FindChineseSubtitle(t.Context(), videoHash())
				if !errors.Is(err, ports.ErrBadResponse) || redirected.Load() != 0 || callback.Load() != 0 {
					t.Fatalf("redirect followed: %v", err)
				}
				if original.Jar != jar || original.CheckRedirect == nil || client.httpClient == original || client.httpClient.Jar != nil {
					t.Fatal("caller client mutated or not isolated")
				}
			})
		}
	}
}

func strconvBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestBadInputsNoRequests(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	for _, hash := range []ports.SubtitleVideoHash{{}, {Hash: "dummy_hash_for_lookup", SizeBytes: 32768}, {Hash: videoHash().Hash, SizeBytes: 16383}, {Hash: strings.ToUpper(videoHash().Hash), SizeBytes: 32768}} {
		_, err := NewClient(s.URL, nil).FindChineseSubtitle(t.Context(), hash)
		if !errors.Is(err, ports.ErrRejected) {
			t.Fatalf("bad hash: %v", err)
		}
	}
	for _, base := range []string{"http://user:secret@" + strings.TrimPrefix(s.URL, "http://"), s.URL + "#fragment", s.URL + "#", "/relative", "file:///secret", "http://%zz"} {
		_, err := NewClient(base, nil).FindChineseSubtitle(t.Context(), videoHash())
		if !errors.Is(err, ports.ErrRejected) {
			t.Fatalf("bad base: %v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("bad input performed I/O")
	}
	c := NewClient("", nil)
	if c.apiURL.String() != defaultAPIURL || c.apiURL.Scheme != "https" || c.httpClient.Timeout <= 0 {
		t.Fatal("unsafe defaults")
	}
}

func TestDownloadValidation(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"english", "1\n00:00:01,000 --> 00:00:04,000\nHello world\n"},
		{"html", "<html>中文</html>"},
		{"ass_disguised_as_srt", "[Script Info]\n中文\n[Events]"},
		{"empty", ""},
		{"oversized", strings.Repeat("x", ports.MaxSubtitleBytes+1)},
		{"gzip_without_encoding", string([]byte{0x1f, 0x8b, 0, 0})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					_, _ = io.WriteString(w, packageJSON("srt", "http://"+r.Host+"/download"))
					return
				}
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, tc.body)
			}))
			defer s.Close()
			got, err := NewClient(s.URL, nil).FindChineseSubtitle(t.Context(), videoHash())
			if got != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("result: %q %v", got, err)
			}
		})
	}
}

func TestHTTPStatuses(t *testing.T) {
	for _, stage := range []string{"lookup", "download"} {
		for _, status := range []int{204, 301, 400, 403, 404, 429, 500, 503} {
			t.Run(stage+"/"+http.StatusText(status), func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stage == "download" && r.Method == http.MethodPost {
						_, _ = io.WriteString(w, packageJSON("srt", "http://"+r.Host+"/download"))
						return
					}
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "secret upstream")
				}))
				defer s.Close()
				got, err := NewClient(s.URL, nil).FindChineseSubtitle(t.Context(), videoHash())
				if got != nil {
					t.Fatal("unexpected result")
				}
				if status == 429 {
					limit, ok := ports.IsRateLimited(err)
					if !ok || limit.RetryAfter != 7*time.Second {
						t.Fatalf("rate limit: %v", err)
					}
				} else if !errors.Is(err, ports.ErrBadResponse) {
					t.Fatalf("status error: %v", err)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatal("body leaked")
				}
			})
		}
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackedBody) Close() error { b.closed.Store(true); return nil }

func TestBodyClosedAndLowLevelErrorsRedacted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		length int64
	}{
		{"success_empty", 200, "[]", 2}, {"invalid_json", 200, "secret", 6},
		{"bad_status", 500, "secret", 6}, {"rate_limit", 429, "secret", 6},
		{"content_length_limit", 200, "", maxAPIBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			c := NewClient("https://www.shooter.cn/api/subapi.php", &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: body, ContentLength: tc.length, Header: make(http.Header)}, nil
			})})
			_, _ = c.FindChineseSubtitle(t.Context(), videoHash())
			if !body.closed.Load() {
				t.Fatal("response body not closed")
			}
		})
	}
	for _, cause := range []error{errors.New("private credential"), context.DeadlineExceeded, context.Canceled} {
		c := NewClient("https://www.shooter.cn/api/subapi.php", &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: "https://user:secret@www.shooter.cn/?nonce=private", Err: cause}
		})})
		_, err := c.FindChineseSubtitle(t.Context(), videoHash())
		want := ports.ErrUnavailable
		if errors.Is(cause, context.DeadlineExceeded) {
			want = ports.ErrTimeout
		}
		if errors.Is(cause, context.Canceled) {
			want = context.Canceled
		}
		if err != want {
			t.Fatalf("error not redacted/native: %v", err)
		}
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	for _, stage := range []string{"lookup", "download"} {
		for _, mode := range []string{"cancel", "timeout"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				started, finished := make(chan struct{}), make(chan struct{})
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stage == "download" && r.Method == http.MethodPost {
						_, _ = io.WriteString(w, packageJSON("srt", "http://"+r.Host+"/download"))
						return
					}
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					close(started)
					<-r.Context().Done()
					close(finished)
				}))
				defer s.Close()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				hc := s.Client()
				if mode == "timeout" {
					hc.Timeout = 100 * time.Millisecond
				}
				c := NewClient(s.URL, hc)
				done := make(chan error, 1)
				go func() { _, err := c.FindChineseSubtitle(ctx, videoHash()); done <- err }()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("request not started")
				}
				if mode == "cancel" {
					cancel()
				}
				select {
				case err := <-done:
					want := ports.ErrTimeout
					if mode == "cancel" {
						want = context.Canceled
					}
					if !errors.Is(err, want) {
						t.Fatalf("error = %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("call still active")
				}
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Fatal("server I/O still active")
				}
			})
		}
	}
}

func TestNoDetachedBodyIO(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	body := &trackedBody{Reader: readerFunc(func([]byte) (int, error) { close(started); <-release; return 0, context.Canceled })}
	c := NewClient("https://www.shooter.cn/api/subapi.php", &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, ContentLength: -1}, nil
	})})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.FindChineseSubtitle(ctx, videoHash()); done <- err }()
	<-started
	cancel()
	select {
	case <-done:
		t.Error("returned before physical read completed")
	default:
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !body.closed.Load() {
			t.Fatalf("cleanup/error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call still active")
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestSelectSRTAfterASS(t *testing.T) {
	var downloads atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			files := []any{map[string]string{"Ext": "ass", "Link": "http://" + r.Host + "/unsupported"}, map[string]string{"Ext": ".SRT", "Link": "http://" + r.Host + "/supported"}}
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"Files": files}})
			return
		}
		downloads.Add(1)
		if r.URL.Path != "/supported" {
			t.Error("selected non-SRT")
		}
		_, _ = io.WriteString(w, chineseSRT)
	}))
	defer s.Close()
	got, err := NewClient(s.URL, nil).FindChineseSubtitle(t.Context(), videoHash())
	if err != nil || string(got) != chineseSRT || downloads.Load() != 1 {
		t.Fatalf("selection: %q %v", got, err)
	}
}

func TestPackageAndFileCaps(t *testing.T) {
	c := NewClient("https://www.shooter.cn/api/subapi.php", nil)
	f := `{"Ext":"srt","Link":"https://www.shooter.cn/api/subapi.php?fetch=secret"}`
	for _, body := range []string{
		`[{"Files":[` + strings.Repeat(f+",", 64) + f + `]}]`,
		`[` + strings.Repeat(`{"Files":[`+strings.TrimSuffix(strings.Repeat(f+",", 64), ",")+`]},`, 4) + `{"Files":[` + f + `]}]`,
		`[{"Files":[` + f + `,{"Ext":null,"Link":null}]}]`,
	} {
		if _, err := c.subtitleLink([]byte(body)); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatalf("unbounded or partially validated response: %v", err)
		}
	}
	// Default HTTPS ports are equivalent, while scheme/port changes are not.
	for _, tc := range []struct {
		link  string
		valid bool
	}{
		{"https://www.shooter.cn:443/api/subapi.php?fetch=x&nonce=a%2Bb", true},
		{"https://www.shooter.cn:444/sub", false}, {"http://www.shooter.cn/sub", false},
	} {
		_, err := c.subtitleLink([]byte(packageJSON("srt", tc.link)))
		if (err == nil) != tc.valid {
			t.Fatalf("origin validation = %v", err)
		}
	}
	normalized, err := media.NormalizeChineseSRT([]byte(chineseSRT))
	if err != nil || !bytes.Equal(normalized, []byte(chineseSRT)) {
		t.Fatal("fixture invalid")
	}
}
