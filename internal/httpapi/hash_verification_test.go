package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMalformedStoredHashHTTPRejectsWithoutSessionOrRewrite(t *testing.T) {
	application, service, router := setupAuthHTTP(t)
	ctx := context.Background()
	const fixtureCredential = "OwnedHashFixture123!"
	user, _, err := service.SetupAdmin(ctx, "owned-hash-fixture", fixtureCredential)
	if err != nil {
		t.Fatal(err)
	}
	pool := application.DBPool()
	var native string
	if err := pool.QueryRow(ctx, "SELECT password_hash FROM app_user WHERE id=$1", user.ID).Scan(&native); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app_session").Scan(&before); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, value string }{
		{"version-tail", strings.Replace(native, "v=19", "v=19junk", 1)},
		{"parameters-tail", strings.Replace(native, "p=4", "p=4junk", 1)},
		{"zero-time", strings.Replace(native, "t=1", "t=0", 1)},
		{"zero-lanes", strings.Replace(native, "p=4", "p=0", 1)},
		{"unsupported-profile", strings.Replace(native, "m=65536", "m=8192", 1)},
		{"overlong", strings.Repeat("x", 4096)},
	}
	body, err := json.Marshal(map[string]string{"username": "owned-hash-fixture", "password": fixtureCredential})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:37891"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, "UPDATE app_user SET password_hash=$1 WHERE id=$2", tc.value, user.ID); err != nil {
				t.Fatal(err)
			}
			rec := request()
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("Set-Cookie") != "" {
				t.Fatal("malformed hash did not produce cookie-free unauthorized response")
			}
			if bytes.Contains(rec.Body.Bytes(), body) || strings.Contains(rec.Body.String(), tc.value) || strings.Contains(rec.Body.String(), fixtureCredential) {
				t.Fatal("credential or stored hash exposed")
			}
			var stored string
			var count int
			if err := pool.QueryRow(ctx, "SELECT password_hash FROM app_user WHERE id=$1", user.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM app_session").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if stored != tc.value || count != before {
				t.Fatal("rejected login changed credential or created session")
			}
		})
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET password_hash=$1 WHERE id=$2", native, user.ID); err != nil {
		t.Fatal(err)
	}
	rec := request()
	if rec.Code != http.StatusOK || rec.Header().Get("Set-Cookie") == "" {
		t.Fatal("native hash no longer permits real login")
	}
	var after int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app_session").Scan(&after); err != nil || after != before+1 {
		t.Fatal("valid login did not create exactly one session")
	}
}
