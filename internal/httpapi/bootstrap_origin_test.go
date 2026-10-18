package httpapi_test

import (
	"bytes"
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBootstrapRejectsBrowserOriginBeforeCredentialEffects(t *testing.T) {
	t.Setenv("PULSE_SETUP_TOKEN", "")
	for _, endpoint := range []string{"setup", "login"} {
		t.Run(endpoint, func(t *testing.T) {
			a, svc, router := setupAuthHTTP(t)
			ctx := context.Background()
			var now time.Time
			if err := a.DBPool().QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
				t.Fatal(err)
			}
			a.Clock().(*clock.MockClock).Set(now)
			const pwd = "SyntheticOriginProbe-5678"
			if endpoint == "login" {
				if _, _, err := svc.SetupAdmin(ctx, "bootstrap-user", pwd); err != nil {
					t.Fatal(err)
				}
			}
			var beforeUsers, beforeSessions int
			if err := a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM app_user),(SELECT count(*) FROM app_session)").Scan(&beforeUsers, &beforeSessions); err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"username":"bootstrap-user","password":"SyntheticOriginProbe-5678"}`)
			req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/auth/"+endpoint, bytes.NewReader(body))
			req.RemoteAddr = "127.0.0.1:3456"
			req.Header.Set("Content-Type", "text/plain") // Browser-simple JSON body does not require CORS preflight.
			req.Header.Set("Origin", "https://foreign.invalid")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("foreign cookie-less %s accepted: status=%d", endpoint, rec.Code)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatal("rejected bootstrap signed cookie")
			}
			var users, sessions int
			if err := a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM app_user),(SELECT count(*) FROM app_session)").Scan(&users, &sessions); err != nil {
				t.Fatal(err)
			}
			if users != beforeUsers || sessions != beforeSessions {
				t.Fatal("rejected bootstrap changed credential state")
			}
		})
	}
}

func TestLocalSetupRejectsNonLocalHost(t *testing.T) {
	t.Setenv("PULSE_SETUP_TOKEN", "")
	a, _, router := setupAuthHTTP(t)
	req := httptest.NewRequest(http.MethodPost, "http://rebound.invalid/api/v1/auth/setup", bytes.NewBufferString(`{"username":"bootstrap-user","password":"SyntheticOriginProbe-5678"}`))
	req.RemoteAddr = "127.0.0.1:3456"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://rebound.invalid")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("nonlocal Host accepted by local setup: %d", rec.Code)
	}
	var users, sessions int
	if err := a.DBPool().QueryRow(context.Background(), "SELECT (SELECT count(*) FROM app_user),(SELECT count(*) FROM app_session)").Scan(&users, &sessions); err != nil {
		t.Fatal(err)
	}
	if users != 0 || sessions != 0 || len(rec.Result().Cookies()) != 0 {
		t.Fatal("nonlocal setup created credentials")
	}
}

func TestDeploymentSetupCredentialClosedAfterOneAdmin(t *testing.T) {
	const deployment = "synthetic-deployment-credential-0123456789"
	t.Setenv("PULSE_SETUP_TOKEN", deployment)
	a, _, router := setupAuthHTTP(t)
	request := func(headers []string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://media.example/api/v1/auth/setup", bytes.NewBufferString(`{"username":"bootstrap-user","password":"SyntheticOriginProbe-5678"}`))
		req.RemoteAddr = "192.0.2.10:3456"
		req.Header.Set("Content-Type", "application/json")
		for _, value := range headers {
			req.Header.Add("X-Setup-Token", value)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, headers := range [][]string{nil, {"incorrect"}, {deployment, "incorrect"}} {
		rec := request(headers)
		if rec.Code != 403 || len(rec.Result().Cookies()) != 0 {
			t.Fatalf("invalid deployment header accepted: status=%d", rec.Code)
		}
	}
	var users, sessions int
	if err := a.DBPool().QueryRow(context.Background(), "SELECT (SELECT count(*) FROM app_user),(SELECT count(*) FROM app_session)").Scan(&users, &sessions); err != nil {
		t.Fatal(err)
	}
	if users != 0 || sessions != 0 {
		t.Fatal("invalid setup credential changed state")
	}
	if rec := request([]string{deployment}); rec.Code != 200 {
		t.Fatalf("explicit deployment setup failed: %d", rec.Code)
	}
	if rec := request([]string{deployment}); rec.Code != 409 || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("setup credential reused: %d", rec.Code)
	}
	if err := a.DBPool().QueryRow(context.Background(), "SELECT (SELECT count(*) FROM app_user),(SELECT count(*) FROM app_session)").Scan(&users, &sessions); err != nil {
		t.Fatal(err)
	}
	if users != 1 || sessions != 1 {
		t.Fatal("deployment setup did not preserve single admin/session")
	}
}
