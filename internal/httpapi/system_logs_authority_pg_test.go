package httpapi_test

import (
	"bufio"
	"context"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSystemLogsCurrentAuthorityPG(t *testing.T) {
	for _, change := range []string{"revoke", "demote", "disable", "expired", "store unavailable"} {
		t.Run(change, func(t *testing.T) {
			hub := httpapi.NewSSEHub()
			defer hub.Close()
			a, token, _, _, _, _, router, _ := setupOperationHTTP(t, func(handler *httpapi.ServerHandler, _ *app.App) { handler.SetLogHub(hub) })
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			session, err := a.Queries().GetSessionByTokenHash(ctx, crypto.HashToken(token))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(router)
			defer server.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/system/logs", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: token})
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != 200 {
				t.Fatal(response.StatusCode)
			}
			reader := bufio.NewReader(response.Body)
			if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, "connected") {
				t.Fatal("missing connection", line, err)
			}
			switch change {
			case "revoke":
				err = a.Queries().DeleteSession(ctx, session.ID)
			case "demote":
				_, err = a.DBPool().Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", session.UserID)
			case "disable":
				_, err = a.DBPool().Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", session.UserID)
			case "expired":
				_, err = a.DBPool().Exec(ctx, "UPDATE app_session SET expires_at=$2 WHERE id=$1", session.ID, a.Clock().Now().Add(-time.Hour))
			case "store unavailable":
				a.DBPool().Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			tail, err := io.ReadAll(reader)
			if err != nil || time.Since(start) > 3*time.Second {
				t.Fatal("revoked stream did not close promptly", err, time.Since(start))
			}
			hub.PublishLog(slog.LevelInfo, "must-not-reach-revoked-stream")
			if strings.Contains(string(tail), "must-not-reach") {
				t.Fatal("revoked stream received data")
			}
		})
	}
}
