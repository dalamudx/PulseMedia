package emby_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/emby"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestEmbyExactRefreshAndBasePath(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/emby/Library/Media/Updated" || r.URL.RawQuery != "" || r.Header.Get("X-Emby-Token") != "owned-private" {
			t.Error("wrong precise endpoint")
		}
		var p struct {
			Updates []struct{ Path, UpdateType string }
		}
		if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.Updates) != 2 || p.Updates[0].Path != "/movies/你好 & +.mkv" || p.Updates[1].Path != `D:\TV\S01E03.mkv` || p.Updates[0].UpdateType != "Created" || p.Updates[1].UpdateType != "Created" {
			t.Error("paths changed or widened")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	client := emby.NewClient(server.URL+"/emby/", "owned-private", server.Client())
	if e := client.RefreshPaths(t.Context(), []string{"/movies/你好 & +.mkv", `D:\TV\S01E03.mkv`, "/movies/你好 & +.mkv"}); e != nil || calls != 1 {
		t.Fatal(e, calls)
	}
	if e := client.RefreshPaths(t.Context(), nil); e != nil || calls != 1 {
		t.Fatal("empty path performed scan")
	}
	for _, p := range []string{"", "movie.mkv", "/movies/../outside.mkv", "/movies/./x.mkv", "/movies/x\n", "/movies/\x00x"} {
		if e := client.RefreshPaths(t.Context(), []string{"/valid.mkv", p}); !errors.Is(e, ports.ErrBadResponse) || calls != 1 {
			t.Fatal("bad path partial side effect", p, e)
		}
	}
}
func TestEmbyCompleteExactLookup(t *testing.T) {
	cases := []struct {
		name, body, id string
		found, bad     bool
	}{
		{"unique", `{"Items":[{"Id":"other","Path":"/movies/other.mkv"},{"Id":"owned","Path":"/movies/你好.mkv"}],"TotalRecordCount":2}`, "owned", true, false},
		{"empty", `{"Items":[],"TotalRecordCount":0}`, "", false, false},
		{"not-matched", `{"Items":[{"Id":"one","Path":"/movies/other.mkv"}],"TotalRecordCount":1}`, "", false, false},
		{"missing", `{}`, "", false, true}, {"null", `{"Items":null,"TotalRecordCount":0}`, "", false, true},
		{"partial", `{"Items":[{"Id":"owned","Path":"/movies/你好.mkv"}],"TotalRecordCount":2}`, "", false, true},
		{"ambiguous", `{"Items":[{"Id":"a","Path":"/movies/你好.mkv"},{"Id":"b","Path":"/movies/你好.mkv"}],"TotalRecordCount":2}`, "", false, true},
		{"id-duplicate", `{"Items":[{"Id":"a","Path":"/movies/你好.mkv"},{"Id":"a","Path":"/movies/other.mkv"}],"TotalRecordCount":2}`, "", false, true},
		{"bad-path", `{"Items":[{"Id":"a","Path":"relative.mkv"}],"TotalRecordCount":1}`, "", false, true},
		{"missing-path", `{"Items":[{"Id":"a"}],"TotalRecordCount":1}`, "", false, true},
		{"alias", `{"Items":[],"items":[],"TotalRecordCount":0}`, "", false, true},
		{"nested-duplicate", `{"Items":[{"Id":"a","Id":"b","Path":"/movies/你好.mkv"}],"TotalRecordCount":1}`, "", false, true},
		{"trailing", `{"Items":[],"TotalRecordCount":0}{}`, "", false, true},
		{"fraction", `{"Items":[],"TotalRecordCount":0.5}`, "", false, true},
		{"html", `<html>login</html>`, "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/Items" || r.URL.Query().Has("Path") || r.URL.Query().Get("Fields") != "Path" || r.URL.Query().Get("EnableTotalRecordCount") != "true" {
					t.Error("unproved path filter")
				}
				_, _ = io.WriteString(w, c.body)
			}))
			defer s.Close()
			id, found, e := emby.NewClient(s.URL, "owned", s.Client()).FindByPath(t.Context(), "/movies/你好.mkv")
			if id != c.id || found != c.found || (e != nil) != c.bad {
				t.Fatal(id, found, e)
			}
		})
	}
}
func TestEmbyProbeRequiresNativeInfo(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `<html>login</html>`, `{"ServerName":"owned"}`, `{"Version":"4.8"}`, `{"ServerName":"owned","Version":"4.8","version":"5"}`, `{"ServerName":"owned","Version":"4.8"}{}`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer s.Close()
			if e := emby.NewClient(s.URL, "owned", s.Client()).Test(t.Context()); !errors.Is(e, ports.ErrBadResponse) {
				t.Fatal(e)
			}
		})
	}
}
func TestEmbyStatusPrivacyAndRateLimit(t *testing.T) {
	for _, code := range []int{301, 302, 401, 403, 404, 429, 500, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(code)
				_, _ = io.WriteString(w, "owned-private-response")
			}))
			defer s.Close()
			client := emby.NewClient(s.URL, "owned-private", s.Client())
			for _, e := range []error{client.Test(t.Context()), client.RefreshPaths(t.Context(), []string{"/movie.mkv"}), func() error { _, _, e := client.FindByPath(t.Context(), "/movie.mkv"); return e }()} {
				want := ports.ErrBadResponse
				if code == 401 || code == 403 {
					want = ports.ErrAuthRequired
				} else if code >= 500 {
					want = ports.ErrUnavailable
				} else if code == 429 {
					var limit *ports.RateLimitError
					if !errors.As(e, &limit) || limit.RetryAfter != 120*time.Second {
						t.Fatal(e)
					}
					continue
				}
				if !errors.Is(e, want) || strings.Contains(e.Error(), "owned-private") {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestEmbyNoRedirectOrInvalidConfigurationIO(t *testing.T) {
	var hits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, second.URL, http.StatusFound) }))
	defer first.Close()
	original := first.Client()
	if e := emby.NewClient(first.URL, "private", original).Test(t.Context()); !errors.Is(e, ports.ErrBadResponse) {
		t.Fatal(e)
	}
	if hits.Load() != 0 || original.CheckRedirect != nil {
		t.Fatal("redirect leaked or mutated client")
	}
	for _, base := range []string{":bad", first.URL + "?key=private", first.URL + "#private", first.URL + "/%2e%2e/escape", first.URL + "/../escape"} {
		if e := emby.NewClient(base, "owned", original).Test(t.Context()); !errors.Is(e, ports.ErrBadResponse) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if e := emby.NewClient(first.URL, "private", original).Test(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestEmbyProxyAuthenticationCompatibility(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "proxy-user" || secret != "proxy-secret" || r.Header.Get("X-Emby-Token") != "api-secret" {
			t.Error("proxy and native credentials must both survive")
		}
		_, _ = io.WriteString(w, `{"ServerName":"owned","Version":"4.8.0"}`)
	}))
	defer s.Close()
	u, e := url.Parse(s.URL)
	if e != nil {
		t.Fatal(e)
	}
	u.User = url.UserPassword("proxy-user", "proxy-secret")
	base := u.String()
	if e := emby.NewClient(base, "api-secret", s.Client()).Test(t.Context()); e != nil {
		t.Fatal(e)
	}
}
func TestEmbyTransportTimeoutAndBounds(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Items" {
			_, _ = io.WriteString(w, strings.Repeat(" ", 8<<20)+`{}`)
			return
		}
		<-r.Context().Done()
	}))
	defer s.Close()
	c := emby.NewClient(s.URL, "private", &http.Client{Timeout: 25 * time.Millisecond})
	if e := c.Test(t.Context()); !errors.Is(e, ports.ErrTimeout) {
		t.Fatal(e)
	}
	_, _, e := emby.NewClient(s.URL, "private", s.Client()).FindByPath(t.Context(), "/movie.mkv")
	if !errors.Is(e, ports.ErrBadResponse) {
		t.Fatal(e)
	}
	unreachable := httptest.NewServer(http.NotFoundHandler())
	raw := unreachable.URL
	unreachable.Close()
	if e := emby.NewClient(raw, "private", nil).Test(t.Context()); !errors.Is(e, ports.ErrUnavailable) || strings.Contains(e.Error(), raw) {
		t.Fatal(e)
	}
}
