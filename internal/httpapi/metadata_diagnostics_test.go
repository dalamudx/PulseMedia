package httpapi_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatadiagnostics"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type httpDiagnosticSource struct {
	calls atomic.Int32
	hook  func()
}

func (s *httpDiagnosticSource) OpenDiagnostic(context.Context) (ports.MetadataDiagnosticSession, error) {
	return s, nil
}
func (s *httpDiagnosticSource) Validate(context.Context) error { return nil }
func (s *httpDiagnosticSource) Probe(_ context.Context, target ports.MetadataDiagnosticTarget) ports.MetadataDiagnosticResult {
	s.calls.Add(1)
	if s.hook != nil {
		s.hook()
	}
	return ports.MetadataDiagnosticResult{Target: target, Category: "available", HostResponded: true}
}
func TestMetadataDiagnosticHTTPAdmissionAndRevocation(t *testing.T) {
	ctx := context.Background()
	a, as, _ := setupAuthHTTP(t)
	user, session, err := as.SetupAdmin(ctx, "diagnostic-admin", "DiagnosticFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	source := &httpDiagnosticSource{}
	handler := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetMetadataDiagnosticsService(metadatadiagnostics.NewService(source))
	server := httptest.NewServer(httpapi.NewRouter(a, handler, as, nil))
	defer server.Close()
	token := "diagnostic-api-token"
	if _, err := a.DBPool().Exec(ctx, "INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'fixture',$3,'[\"admin\"]')", uuid.New(), user.ID, crypto.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	send := func(method, path, body, identity, origin string) (int, string) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		switch identity {
		case "cookie":
			request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		case "duplicate-cookie":
			request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
			request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		case "token":
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
			t.Errorf("response cacheable: %s", response.Header.Get("Cache-Control"))
		}
		if strings.Contains(string(raw), "DiagnosticFixture") || strings.Contains(string(raw), token) {
			t.Fatal("unsafe response")
		}
		return response.StatusCode, string(raw)
	}
	const path = "/api/v1/settings/metadata-diagnostics"
	for _, tc := range []struct {
		name, method, path, body, identity, origin string
		want                                       int
	}{
		{"anonymous", "POST", path, `{"target":"tmdb_api"}`, "", server.URL, 401},
		{"token", "POST", path, `{"target":"tmdb_api"}`, "token", server.URL, 403},
		{"CSRF absent", "POST", path, `{"target":"tmdb_api"}`, "cookie", "", 403},
		{"CSRF foreign", "POST", path, `{"target":"tmdb_api"}`, "cookie", "https://foreign.invalid", 403},
		{"duplicate cookie", "POST", path, `{"target":"tmdb_api"}`, "duplicate-cookie", server.URL, 403},
		{"GET forbidden", "GET", path, "", "cookie", server.URL, 405},
		{"query forbidden", "POST", path + "?target=tmdb_api", `{"target":"tmdb_api"}`, "cookie", server.URL, 400},
		{"empty query forbidden", "POST", path + "?", `{"target":"tmdb_api"}`, "cookie", server.URL, 400},
		{"duplicate target", "POST", path, `{"target":"tmdb_api","target":"tmdb_image"}`, "cookie", server.URL, 400},
		{"escaped duplicate", "POST", path, `{"target":"tmdb_api","targ\u0065t":"tmdb_image"}`, "cookie", server.URL, 400},
		{"URL forbidden", "POST", path, `{"target":"https://private.invalid/secret"}`, "cookie", server.URL, 400},
		{"extra URL forbidden", "POST", path, `{"target":"tmdb_api","url":"https://private.invalid"}`, "cookie", server.URL, 400},
		{"nested forbidden", "POST", path, `{"target":{"target":"tmdb_api"}}`, "cookie", server.URL, 400},
		{"trailing JSON", "POST", path, `{"target":"tmdb_api"}{}`, "cookie", server.URL, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := send(tc.method, tc.path, tc.body, tc.identity, tc.origin)
			if code != tc.want {
				t.Fatalf("status=%d body=%s", code, raw)
			}
			if source.calls.Load() != 0 {
				t.Fatal("unauthorized network call")
			}
		})
	}
	if _, err := a.DBPool().Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	if code, body := send("POST", path, `{"target":"tmdb_api"}`, "cookie", server.URL); code != 403 {
		t.Fatal(code, body)
	}
	if source.calls.Load() != 0 {
		t.Fatal("member request reached network")
	}
	if _, err := a.DBPool().Exec(ctx, "UPDATE app_user SET role='admin' WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	if code, body := send("POST", path, `{"target":"tmdb_api"}`, "cookie", server.URL); code != 200 || !strings.Contains(body, `"category":"available"`) {
		t.Fatal(code, body)
	}
	if source.calls.Load() != 1 {
		t.Fatal("expected single explicit probe")
	}
	source.hook = func() {
		if _, err := a.DBPool().Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", user.ID); err != nil {
			t.Error(err)
		}
	}
	if code, body := send("POST", path, `{"target":"tmdb_api"}`, "cookie", server.URL); code != 403 || strings.Contains(body, `"available"`) {
		t.Fatal("revocation exposed result", code, body)
	}
	if source.calls.Load() != 2 {
		t.Fatal("unexpected probe count")
	}
}
