package tmdb

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatadiagnostics"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type diagnosticTransport struct {
	base    *url.URL
	next    http.RoundTripper
	inspect func(*http.Request)
}

func (d diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if d.inspect != nil {
		d.inspect(r)
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = d.base.Scheme
	u.Host = d.base.Host
	clone.URL = &u
	clone.Host = d.base.Host
	return d.next.RoundTrip(clone)
}
func diagnosticHTTP(t *testing.T, h http.HandlerFunc, timeout time.Duration, inspect func(*http.Request)) *http.Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Timeout: timeout, Transport: diagnosticTransport{target, transport, inspect}}
}
func TestMetadataDiagnosticControlledHTTP(t *testing.T) {
	var pngBody bytes.Buffer
	if err := png.Encode(&pngBody, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		target         ports.MetadataDiagnosticTarget
		status         int
		body, category string
		image          bool
	}{
		{"API", ports.DiagnosticTMDBAPI, 200, `{"id":550,"title":"Fight Club"}`, "available", false},
		{"wrong identity", ports.DiagnosticTMDBAPI, 200, `{"id":551,"title":"wrong"}`, "bad_response", false},
		{"raw secret body", ports.DiagnosticTMDBAPI, 200, "https://secret.invalid?api_key=supersecret", "bad_response", false},
		{"authentication", ports.DiagnosticTMDBAPI, 401, "supersecret", "auth_required", false},
		{"forbidden", ports.DiagnosticTMDBAPI, 403, "supersecret", "auth_required", false},
		{"limited", ports.DiagnosticTMDBAPI, 429, "supersecret", "rate_limited", false},
		{"redirect", ports.DiagnosticTMDBAPI, 302, "supersecret", "host_response", false},
		{"server error", ports.DiagnosticTMDBAPI, 503, "supersecret", "host_response", false},
		{"image", ports.DiagnosticTMDBImage, 200, pngBody.String(), "available", true},
		{"image host only", ports.DiagnosticTMDBImage, 404, "not an image", "host_response", false},
		{"HTML image", ports.DiagnosticTMDBImage, 200, "<html>supersecret</html>", "bad_response", false},
		{"truncated image", ports.DiagnosticTMDBImage, 200, pngBody.String()[:len(pngBody.String())-20], "bad_response", false},
		{"oversized", ports.DiagnosticTMDBImage, 200, strings.Repeat("x", (8<<20)+1), "bad_response", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := diagnosticHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "http://unowned.invalid/secret")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}, time.Second, func(r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Cookie") != "" {
					t.Errorf("unexpected request")
				}
				if tc.target == ports.DiagnosticTMDBAPI {
					if r.URL.Host != "api.themoviedb.org" || r.URL.Path != "/3/movie/550" || r.URL.Query().Get("api_key") != "supersecret" {
						t.Errorf("wrong fixed API target")
					}
				} else if r.URL.String() != diagnosticPoster || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
					t.Errorf("image credentials or target changed")
				}
			})
			c := NewClient("supersecret", WithHTTPClient(client), WithRequestBudget(budget.NewMetadataBudget(nil)))
			result := c.ProbeDiagnostic(context.Background(), tc.target)
			if result.Category != tc.category || !result.HostResponded || result.ImageRetrieved != tc.image || calls.Load() != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "supersecret") || strings.Contains(string(raw), "http") {
				t.Fatal("unsafe result")
			}
		})
	}
}
func TestMetadataDiagnosticCancellationAndTimeout(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelled], func(t *testing.T) {
			started := make(chan struct{})
			client := diagnosticHTTP(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }, 40*time.Millisecond, nil)
			c := NewClient("supersecret", WithHTTPClient(client))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan ports.MetadataDiagnosticResult, 1)
			go func() { done <- c.ProbeDiagnostic(ctx, ports.DiagnosticTMDBAPI) }()
			<-started
			if cancelled {
				cancel()
			}
			result := <-done
			expected := "timeout"
			if cancelled {
				expected = "cancelled"
			}
			if result.Category != expected || result.ImageRetrieved {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

type diagnosticSettings struct {
	mu      sync.Mutex
	version int64
	secret  string
}

func (s *diagnosticSettings) GetSetting(context.Context, string) (*ports.SettingItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &ports.SettingItem{Key: "tmdb.api_key", Version: s.version}, nil
}
func (s *diagnosticSettings) GetSecret(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secret, nil
}
func TestMetadataDiagnosticDiscardsConfigurationChange(t *testing.T) {
	for _, change := range []string{"version", "secret", "environment"} {
		t.Run(change, func(t *testing.T) {
			settings := &diagnosticSettings{version: 1, secret: "initial-secret"}
			var environment atomic.Value
			environment.Store("")
			started, release := make(chan struct{}), make(chan struct{})
			client := diagnosticHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-release
				_, _ = io.WriteString(w, `{"id":550,"title":"Fight Club"}`)
			}, time.Second, nil)
			source := NewDiagnosticSource(settings, func() string { return environment.Load().(string) }, client, budget.NewMetadataBudget(nil))
			svc := metadatadiagnostics.NewService(source)
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: "11111111-1111-4111-8111-111111111111", Role: ports.RoleAdmin}
			done := make(chan ports.MetadataDiagnosticResult, 1)
			go func() {
				result, err := svc.Run(context.Background(), actor, ports.DiagnosticTMDBAPI)
				if err != nil {
					t.Error(err)
				}
				done <- result
			}()
			<-started
			settings.mu.Lock()
			switch change {
			case "version":
				settings.version++
			case "secret":
				settings.secret = "new-secret"
			case "environment":
				environment.Store("new-environment-secret")
			}
			settings.mu.Unlock()
			close(release)
			result := <-done
			if result.Category != "configuration_changed" || result.HostResponded || result.ImageRetrieved {
				t.Fatalf("stale evidence=%+v", result)
			}
		})
	}
}
func TestMetadataDiagnosticSharedBudgetAndSafeFailure(t *testing.T) {
	b := budget.NewMetadataBudget(nil)
	b.RecordRateLimit(time.Minute)
	var calls atomic.Int32
	client := diagnosticHTTP(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }, time.Second, nil)
	result := NewClient("secret", WithHTTPClient(client), WithRequestBudget(b)).ProbeDiagnostic(context.Background(), ports.DiagnosticTMDBAPI)
	if result.Category != "rate_limited" || calls.Load() != 0 {
		t.Fatalf("budget bypass: %+v", result)
	}
}
