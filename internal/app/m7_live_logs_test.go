package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

type logFrame struct {
	At    string `json:"at"`
	Level string `json:"level"`
	Line  string `json:"line"`
}

// Real Cookie/App/PostgreSQL drive of the live log stream: the process logger
// publishes into the hub, and only administrators may read it.
func TestM7LiveLogsAppHTTPPG(t *testing.T) {
	f := organizationSetup(t)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.LogLevel = "debug"
	a, err := New(cfg, WithDBPool(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if a.sseHub == nil {
		t.Fatal("app did not create the live log hub")
	}

	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	password := fmt.Sprintf("Logs-%s-123!", t.Name())
	admin, _, err := authService.SetupAdmin(t.Context(), "logs-admin", password)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewServerHandler(a, authService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(a, handler, authService, a.sseHub)

	loginBody, _ := json.Marshal(map[string]string{"username": "logs-admin", "password": password})
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(loginBody)))
	login.Header.Set("Content-Type", "application/json")
	login.Host, login.RemoteAddr = "localhost", "127.0.0.1:1234"
	loginReply := httptest.NewRecorder()
	router.ServeHTTP(loginReply, login)
	if loginReply.Code != http.StatusOK || len(loginReply.Result().Cookies()) == 0 {
		t.Fatalf("real login failed: %d", loginReply.Code)
	}
	cookie := loginReply.Result().Cookies()[0]

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	open := func(t *testing.T, path string, authenticated bool) *http.Response {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		t.Cleanup(cancel)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			request.AddCookie(cookie)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	frames := func(t *testing.T, response *http.Response, want func([]logFrame) bool) []logFrame {
		t.Helper()
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var seen []logFrame
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			var record logFrame
			if json.Unmarshal([]byte(payload), &record) != nil || record.Level == "" {
				continue // connected frame
			}
			seen = append(seen, record)
			if want(seen) {
				return seen
			}
		}
		return seen
	}

	t.Run("anonymous and member are refused", func(t *testing.T) {
		if response := open(t, "/api/v1/system/logs", false); response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d", response.StatusCode)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, admin.ID); err != nil {
			t.Fatal(err)
		}
		response := open(t, "/api/v1/system/logs", true)
		if response.StatusCode != http.StatusForbidden || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/problem+json") {
			t.Fatalf("member: %d %s", response.StatusCode, response.Header.Get("Content-Type"))
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE app_user SET role='admin' WHERE id=$1`, admin.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("admin receives redacted live records", func(t *testing.T) {
		response := open(t, "/api/v1/system/logs?level=INFO", true)
		if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("admin: %d %s", response.StatusCode, response.Header.Get("Content-Type"))
		}
		// The process logger publishes through the app's own hub wiring.
		a.logger.Warn("source fetch failed", slog.String("url", "https://user:{{PASSWORD_b7k2m4pz}}@example.invalid/?api_key=abcdef"), slog.String("attempt", "1"))
		seen := frames(t, response, func(records []logFrame) bool {
			for _, record := range records {
				if strings.Contains(record.Line, "source fetch failed") {
					return true
				}
			}
			return false
		})
		var record logFrame
		found := false
		for _, candidate := range seen {
			if strings.Contains(candidate.Line, "source fetch failed") {
				record, found = candidate, true
				break
			}
		}
		if !found {
			t.Fatalf("no log frame reached the admin stream: %+v", seen)
		}
		if record.Level != "WARN" {
			t.Fatalf("level %q", record.Level)
		}
		for _, secret := range []string{"secret", "abcdef"} {
			if strings.Contains(record.Line, secret) {
				t.Fatalf("stream leaked %q: %s", secret, record.Line)
			}
		}
		if strings.TrimSpace(record.At) == "" {
			t.Fatal("record lost its timestamp")
		}
	})

	t.Run("level filter suppresses lower records", func(t *testing.T) {
		response := open(t, "/api/v1/system/logs?level=ERROR", true)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("admin: %d", response.StatusCode)
		}
		a.logger.Info("informational line that must not appear")
		a.logger.Error("fatal line that must appear")
		seen := frames(t, response, func(records []logFrame) bool {
			for _, record := range records {
				if strings.Contains(record.Line, "fatal line that must appear") {
					return true
				}
			}
			return false
		})
		joined := ""
		for _, record := range seen {
			joined += record.Line + "\n"
			if record.Level != "ERROR" {
				t.Fatalf("filter leaked %q", record.Level)
			}
		}
		if strings.Contains(joined, "informational line that must not appear") {
			t.Fatal("INFO line passed an ERROR filter")
		}
	})
}
