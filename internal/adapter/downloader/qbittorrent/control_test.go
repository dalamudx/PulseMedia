package qbittorrent

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeControlQBitVersionGate(t *testing.T) {
	for _, version := range []string{"v4.6.7", "v5.0.0", "v6.0.0", "<html>login</html>"} {
		t.Run(version, func(t *testing.T) {
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = fmt.Fprint(w, "Ok.")
				case "/api/v2/app/version":
					_, _ = fmt.Fprint(w, version)
				default:
					mutations.Add(1)
					if r.Method != "POST" {
						t.Error(r.Method)
					}
					_ = r.ParseForm()
					if r.Form.Get("hashes") != strings.Repeat("a", 40) || len(r.Form) != 1 {
						t.Error("not exact hash", r.Form)
					}
					action := "pause"
					if strings.HasPrefix(version, "v5") {
						action = "stop"
					}
					if r.URL.Path != "/api/v2/torrents/"+action && r.URL.Path != "/api/v2/torrents/"+map[string]string{"pause": "resume", "stop": "start"}[action] {
						t.Error(r.URL.Path)
					}
				}
			}))
			defer server.Close()
			c, e := NewClient(server.URL, "user", "secret", server.Client())
			if e != nil {
				t.Fatal(e)
			}
			for _, paused := range []bool{true, false} {
				e = c.SetTorrentPaused(t.Context(), strings.Repeat("A", 40), paused)
				supported := strings.HasPrefix(version, "v4") || strings.HasPrefix(version, "v5")
				if (e == nil) != supported {
					t.Fatal(version, e)
				}
			}
			expected := int32(0)
			if strings.HasPrefix(version, "v4") || strings.HasPrefix(version, "v5") {
				expected = 2
			}
			if mutations.Load() != expected {
				t.Fatal("capability gate sent", mutations.Load())
			}
		})
	}
}

type controlRoundTripper func(*http.Request) (*http.Response, error)

func (f controlRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestNativeControlQBitUnknownNeverReplays(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: controlRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "login") {
			return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		if strings.HasSuffix(r.URL.Path, "version") {
			return &http.Response{StatusCode: 200, Body: ioControl("v5.0.0"), Header: make(http.Header)}, nil
		}
		calls++
		if r.GetBody != nil {
			t.Error("native mutation replayable")
		}
		return nil, errors.New("secret endpoint outcome unknown")
	})}
	c, _ := NewClient("https://control.invalid", "", "", client)
	c.loggedIn = true
	e := c.SetTorrentPaused(context.Background(), strings.Repeat("a", 40), true)
	if e == nil || calls != 1 || strings.Contains(e.Error(), "secret") {
		t.Fatal(calls, e)
	}
	if e = c.SetTorrentPaused(t.Context(), "all", true); !errors.Is(e, ports.ErrDownloadControlInvalid) {
		t.Fatal(e)
	}
}

type controlBody struct{ *strings.Reader }

func (controlBody) Close() error        { return nil }
func ioControl(text string) controlBody { return controlBody{strings.NewReader(text)} }
