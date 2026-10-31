package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mp23HTTPRoute struct {
	base      *url.URL
	transport http.RoundTripper
}

func (r mp23HTTPRoute) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "api.themoviedb.org" && request.URL.Host != "image.tmdb.org" {
		return nil, errors.New("unapproved metadata target")
	}
	next := request.Clone(request.Context())
	u := *request.URL
	u.Scheme = r.base.Scheme
	u.Host = r.base.Host
	next.URL = &u
	next.Host = r.base.Host
	return r.transport.RoundTrip(next)
}
func TestMetadataDiagnosticProductionApp(t *testing.T) {
	t.Setenv("TMDB_API_KEY", "")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	observer, dsn := bootstrapTestPool(t)
	ctx := context.Background()
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	var mu sync.Mutex
	var hold, entered chan struct{}
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if strings.HasPrefix(r.URL.Path, "/t/p/w500/") {
			if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
				t.Error("image credential leak")
			}
			_, _ = w.Write(imageData.Bytes())
			return
		}
		if r.URL.Path != "/3/movie/550" {
			t.Error("wrong API path")
			w.WriteHeader(500)
			return
		}
		key := r.URL.Query().Get("api_key")
		if key != "app-diagnostic-key-one" && key != "app-diagnostic-key-two" {
			t.Error("wrong production credential")
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		waiting, reached := hold, entered
		mu.Unlock()
		if waiting != nil {
			close(reached)
			<-waiting
		}
		_, _ = io.WriteString(w, `{"id":550,"title":"Fight Club"}`)
	}))
	defer native.Close()
	target, _ := url.Parse(native.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.Database.URL = dsn
	cfg.SecretKey = strings.Repeat("a", 64)
	cfg.Server.HTTPAddr = address
	application, err := New(cfg, WithMetadataHTTPClient(&http.Client{Transport: mp23HTTPRoute{target, transport}, Timeout: 3 * time.Second}), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	as := auth.NewService(postgres.NewAuthenticationStore(application.DBPool()), crypto.AuthenticationSecrets{}, application.Clock(), application.IDGen())
	user, session, err := as.SetupAdmin(ctx, "mp23-app-admin", "AppDiagnosticFixture123!")
	if err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
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
		if err := application.Close(); err != nil {
			t.Error(err)
		}
	})
	base := "http://" + address
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		request, _ := http.NewRequestWithContext(ctx, "GET", base+"/healthz", nil)
		response, e := client.Do(request)
		if e == nil {
			_ = response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("App HTTP not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	send := func(method, path, body string) (int, []byte) {
		request, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", base)
		request.Header.Set("Idempotency-Key", uuid.NewString())
		request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		response, err := client.Do(request)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(raw), "app-diagnostic-key") || strings.Contains(string(raw), "api_key=") {
			t.Error("credential exposed")
		}
		return response.StatusCode, raw
	}
	probe := func(target string) ports.MetadataDiagnosticResult {
		t.Helper()
		code, raw := send("POST", "/api/v1/settings/metadata-diagnostics", `{"target":"`+target+`"}`)
		var result ports.MetadataDiagnosticResult
		if code != 200 || json.Unmarshal(raw, &result) != nil {
			t.Fatalf("probe status=%d body=%s", code, raw)
		}
		return result
	}
	// Startup and health reads do not probe external metadata.
	if count.Load() != 0 {
		t.Fatal("automatic diagnostic request")
	}
	if result := probe("tmdb_api"); result.Category != "not_configured" {
		t.Fatalf("unconfigured=%+v", result)
	}
	if count.Load() != 0 {
		t.Fatal("unconfigured API reached network")
	}
	if result := probe("tmdb_image"); result.Category != "available" || !result.ImageRetrieved {
		t.Fatalf("image without API credential=%+v", result)
	}
	save := func(version int, key string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"expected_version": version, "value": map[string]string{"api_key": key}})
		code, raw := send("PUT", "/api/v1/settings/tmdb.api_key", string(body))
		if code != 200 {
			t.Fatalf("save status=%d body=%s", code, raw)
		}
	}
	save(0, "app-diagnostic-key-one")
	if result := probe("tmdb_api"); result.Category != "available" {
		t.Fatalf("configured API=%+v", result)
	}
	mu.Lock()
	hold, entered = make(chan struct{}), make(chan struct{})
	waiting, reached := hold, entered
	mu.Unlock()
	resultCh := make(chan struct {
		code int
		raw  []byte
	}, 1)
	go func() {
		code, raw := send("POST", "/api/v1/settings/metadata-diagnostics", `{"target":"tmdb_api"}`)
		resultCh <- struct {
			code int
			raw  []byte
		}{code, raw}
	}()
	<-reached
	save(1, "app-diagnostic-key-two")
	close(waiting)
	response := <-resultCh
	var result ports.MetadataDiagnosticResult
	if response.code != 200 || json.Unmarshal(response.raw, &result) != nil || result.Category != "configuration_changed" || result.HostResponded || result.ImageRetrieved {
		t.Fatalf("stale production snapshot code=%d result=%+v", response.code, result)
	}
	mu.Lock()
	hold = nil
	entered = nil
	mu.Unlock()
	var mediaCount, fileCount int
	if err := observer.QueryRow(ctx, "SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM library_file)").Scan(&mediaCount, &fileCount); err != nil || mediaCount != 0 || fileCount != 0 {
		t.Fatalf("diagnostic wrote media/files: %d/%d err=%v", mediaCount, fileCount, err)
	}
	if _, err := observer.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	before := count.Load()
	code, raw := send("POST", "/api/v1/settings/metadata-diagnostics", `{"target":"tmdb_api"}`)
	if code != 403 || count.Load() != before {
		t.Fatalf("demoted production request code=%d raw=%s", code, raw)
	}
}
