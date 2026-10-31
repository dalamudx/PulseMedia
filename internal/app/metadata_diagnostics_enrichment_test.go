package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/fanart"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tvdb"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
)

type enrichmentBudget struct {
	active  atomic.Int32
	limited atomic.Int32
}

func (b *enrichmentBudget) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.active.Add(1)
	return func() { b.active.Add(-1) }, nil
}
func (b *enrichmentBudget) RecordRateLimit(time.Duration) { b.limited.Add(1) }

type enrichmentPeer func(*http.Request) (*http.Response, error)

func (f enrichmentPeer) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func enrichmentProbe(target ports.MetadataDiagnosticTarget) func(context.Context, *http.Client) error {
	return func(ctx context.Context, c *http.Client) error {
		if target == ports.DiagnosticTVDBAPI {
			return tvdb.NewClientWithPIN("fixture-key", "fixture-pin", c).ProbeDiagnosticAuthentication(ctx)
		}
		_, err := fanart.NewClient("fixture-key", c).GetArtworks(ctx, ports.ArtworkIdentity{MediaType: "movie", Provider: "tmdb", ID: 550})
		return err
	}
}
func enrichmentReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestEnrichmentDiagnosticNativeSafety(t *testing.T) {
	for _, target := range []ports.MetadataDiagnosticTarget{ports.DiagnosticFanartAPI, ports.DiagnosticTVDBAPI} {
		for _, tc := range []struct {
			name           string
			status         int
			body, category string
		}{
			{"available", 200, `{"tmdb_id":"550"}`, "available"},
			{"bad-json", 200, `secret-invalid`, "bad_response"},
			{"auth", 401, `fixture-key`, "auth_required"},
			{"rate", 429, `fixture-key`, "rate_limited"},
			{"redirect", 302, `fixture-key`, "bad_response"},
			{"oversize", 200, strings.Repeat("x", (5<<20)+1), "bad_response"},
		} {
			t.Run(string(target)+"/"+tc.name, func(t *testing.T) {
				budget := &enrichmentBudget{}
				var calls int
				client := &http.Client{Transport: enrichmentPeer(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
						t.Error("credential forwarded in header")
					}
					body := tc.body
					if target == ports.DiagnosticFanartAPI {
						if r.Method != "GET" || r.URL.String() != "https://webservice.fanart.tv/v3/movies/550?api_key=fixture-key" {
							t.Error("wrong fixed Fanart target")
						}
					} else {
						if r.Method != "POST" || r.URL.String() != "https://api4.thetvdb.com/v4/login" {
							t.Error("wrong fixed TVDB target")
						}
						var credentials map[string]string
						if json.NewDecoder(r.Body).Decode(&credentials) != nil || credentials["apikey"] != "fixture-key" || credentials["pin"] != "fixture-pin" || len(credentials) != 2 {
							t.Error("wrong native login")
						}
						if tc.name == "available" {
							body = `{"status":"success","data":{"token":"fixture-token"}}`
						}
					}
					response := enrichmentReply(tc.status, body)
					response.Header.Set("Location", "https://forbidden.invalid/credential")
					response.Header.Set("Retry-After", "30")
					return response, nil
				})}
				result := runEnrichmentDiagnostic(t.Context(), target, client, budget, enrichmentProbe(target), func(context.Context) error { return nil })
				if result.Category != tc.category || !result.HostResponded || result.ImageRetrieved || calls != 1 || budget.active.Load() != 0 {
					t.Fatalf("result=%+v calls=%d active=%d", result, calls, budget.active.Load())
				}
				if tc.status == 429 && budget.limited.Load() != 1 {
					t.Fatal("rate advice lost")
				}
				raw, _ := json.Marshal(result)
				if strings.Contains(string(raw), "fixture-") {
					t.Fatal("credential disclosure")
				}
			})
		}
	}
}

type enrichmentClose struct {
	io.Reader
	entered, release chan struct{}
	fail             bool
}

func (b enrichmentClose) Close() error {
	if b.entered != nil {
		close(b.entered)
		<-b.release
	}
	if b.fail {
		return errors.New("private-credential")
	}
	return nil
}
func TestEnrichmentDiagnosticPhysicalClose(t *testing.T) {
	for _, target := range []ports.MetadataDiagnosticTarget{ports.DiagnosticFanartAPI, ports.DiagnosticTVDBAPI} {
		t.Run(string(target), func(t *testing.T) {
			budget := &enrichmentBudget{}
			entered, release := make(chan struct{}), make(chan struct{})
			client := &http.Client{Transport: enrichmentPeer(func(*http.Request) (*http.Response, error) {
				response := enrichmentReply(401, "")
				response.Body = enrichmentClose{Reader: strings.NewReader(""), entered: entered, release: release}
				return response, nil
			})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan ports.MetadataDiagnosticResult, 1)
			go func() {
				done <- runEnrichmentDiagnostic(ctx, target, client, budget, enrichmentProbe(target), func(context.Context) error { return nil })
			}()
			<-entered
			cancel()
			if budget.active.Load() != 1 {
				t.Error("physical close lost budget")
			}
			select {
			case <-done:
				t.Error("returned during close")
			default:
			}
			close(release)
			if result := <-done; result.Category != "cancelled" || budget.active.Load() != 0 {
				t.Fatalf("result=%+v", result)
			}
			client.Transport = enrichmentPeer(func(*http.Request) (*http.Response, error) {
				response := enrichmentReply(401, "")
				response.Body = enrichmentClose{Reader: strings.NewReader(""), fail: true}
				return response, nil
			})
			if result := runEnrichmentDiagnostic(t.Context(), target, client, budget, enrichmentProbe(target), func(context.Context) error { return nil }); result.Category != "unavailable" {
				t.Fatalf("close failure=%+v", result)
			}
		})
	}
}

func TestEnrichmentDiagnosticEncryptedAppHTTP(t *testing.T) {
	_, dsn := bootstrapTestPool(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.Server.HTTPAddr = address
	cfg.DataDir = t.TempDir()
	cfg.Database.URL = dsn
	cfg.SecretKey = strings.Repeat("a", 64)
	application, err := New(cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Error(err)
		}
	})
	as := auth.NewService(postgres.NewAuthenticationStore(application.DBPool()), crypto.AuthenticationSecrets{}, application.Clock(), application.IDGen())
	_, session, err := as.SetupAdmin(t.Context(), "mp23-enrichment-admin", "EnrichmentFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := as.AuthenticateToken(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(postgres.NewSettingsStore(application.DBPool(), application.IDGen()), crypto.NewSettingsCipher(bytes.Repeat([]byte{0xaa}, 32)))
	versions := map[string]int64{}
	save := func(key string, value any) {
		t.Helper()
		result, err := svc.UpdateSetting(t.Context(), actor, key, settings.UpdateSettingRequest{Value: value, ExpectedVersion: versions[key]}, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		versions[key] = result.Data.Version
	}
	var calls atomic.Int32
	var change atomic.Pointer[func()]
	native := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if hook := change.Load(); hook != nil {
			(*hook)()
		}
		switch r.URL.Path {
		case "/v3/movies/550":
			if r.URL.Query().Get("api_key") != "encrypted-fanart-key" {
				t.Error("wrong decrypted Fanart key")
			}
			_, _ = io.WriteString(w, `{"tmdb_id":"550"}`)
		case "/v4/login":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["apikey"] != "encrypted-tvdb-key" || body["pin"] != "encrypted-pin" {
				t.Error("wrong decrypted TVDB credentials")
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"token":"private-token"}}`)
		default:
			t.Error("unexpected outbound path")
			w.WriteHeader(500)
		}
	}))
	defer native.Close()
	peer, _ := url.Parse(native.URL)
	client := native.Client()
	base := client.Transport
	client.Transport = enrichmentPeer(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "webservice.fanart.tv" && r.URL.Host != "api4.thetvdb.com" {
			return nil, errors.New("forbidden host")
		}
		next := r.Clone(r.Context())
		u := *r.URL
		u.Host = peer.Host
		next.URL = &u
		next.Host = peer.Host
		return base.RoundTrip(next)
	})
	application.metadataHTTPClient = client
	runCtx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- application.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("App did not drain")
		}
	})
	baseURL := "http://" + address
	httpClient := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		request, _ := http.NewRequestWithContext(t.Context(), "GET", baseURL+"/healthz", nil)
		response, err := httpClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("App HTTP not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	probe := func(target string, origin bool) (int, ports.MetadataDiagnosticResult) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), "POST", baseURL+"/api/v1/settings/metadata-diagnostics", strings.NewReader(`{"target":"`+target+`"}`))
		req.Header.Set("Content-Type", "application/json")
		if origin {
			req.Header.Set("Origin", baseURL)
		}
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(response.Header.Get("Cache-Control"), "no-store") || strings.Contains(string(raw), "encrypted-") || strings.Contains(string(raw), "private-token") {
			t.Fatal("unsafe diagnostic response")
		}
		var result ports.MetadataDiagnosticResult
		_ = json.Unmarshal(raw, &result)
		return response.StatusCode, result
	}
	for _, target := range []string{"fanart_api", "tvdb_api"} {
		if code, result := probe(target, true); code != 200 || result.Category != "not_configured" {
			t.Fatalf("unconfigured %s: status=%d result=%+v", target, code, result)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unconfigured source called network")
	}
	save("fanart.api_key", map[string]any{"api_key": "encrypted-fanart-key"})
	save("fanart.artwork", map[string]any{"enabled": true})
	save("tvdb.credentials", map[string]any{"api_key": "encrypted-tvdb-key", "pin": "encrypted-pin"})
	save("tvdb.fallback", map[string]any{"enabled": true})
	for _, target := range []string{"fanart_api", "tvdb_api"} {
		if code, result := probe(target, true); code != 200 || result.Category != "available" || !result.HostResponded || result.ImageRetrieved {
			t.Fatalf("configured %s: status=%d result=%+v", target, code, result)
		}
		before := calls.Load()
		if code, _ := probe(target, false); code != 403 || calls.Load() != before {
			t.Fatal("CSRF reached native source")
		}
	}
	for _, tc := range []struct{ target, key string }{{"fanart_api", "fanart.artwork"}, {"tvdb_api", "tvdb.fallback"}} {
		hook := func() { save(tc.key, map[string]any{"enabled": false}) }
		change.Store(&hook)
		if code, result := probe(tc.target, true); code != 200 || result.Category != "configuration_changed" || result.HostResponded {
			t.Fatalf("late configuration %s: status=%d result=%+v", tc.target, code, result)
		}
		change.Store(nil)
	}
	for _, tc := range []struct {
		target, key       string
		original, rotated any
	}{
		{"fanart_api", "fanart.api_key", map[string]any{"api_key": "encrypted-fanart-key"}, map[string]any{"api_key": "rotated-fanart-key"}},
		{"tvdb_api", "tvdb.credentials", map[string]any{"api_key": "encrypted-tvdb-key", "pin": "encrypted-pin"}, map[string]any{"api_key": "rotated-tvdb-key", "pin": "rotated-pin"}},
	} {
		save("fanart.artwork", map[string]any{"enabled": true})
		save("tvdb.fallback", map[string]any{"enabled": true})
		hook := func() { save(tc.key, tc.rotated) }
		change.Store(&hook)
		if code, result := probe(tc.target, true); code != 200 || result.Category != "configuration_changed" || result.HostResponded {
			t.Fatalf("rotated %s: status=%d result=%+v", tc.target, code, result)
		}
		change.Store(nil)
		save(tc.key, tc.original)
	}
	for _, target := range []string{"fanart_api", "tvdb_api"} {
		hook := func() {
			if _, err := application.DBPool().Exec(t.Context(), "UPDATE app_user SET role='member' WHERE id=$1", actor.ID); err != nil {
				t.Error(err)
			}
		}
		change.Store(&hook)
		if code, _ := probe(target, true); code != 403 {
			t.Fatalf("revocation %s: status=%d", target, code)
		}
		change.Store(nil)
		before := calls.Load()
		if code, _ := probe(target, true); code != 403 || calls.Load() != before {
			t.Fatal("revoked user reached source")
		}
		if _, err := application.DBPool().Exec(t.Context(), "UPDATE app_user SET role='admin' WHERE id=$1", actor.ID); err != nil {
			t.Fatal(err)
		}
	}
}
