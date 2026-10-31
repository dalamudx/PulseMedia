package httpapi_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountSecurityActualRouterPG(t *testing.T) {
	a, _, _ := setupAuthHTTP(t)
	ctx := t.Context()
	secrets := crypto.AuthenticationSecrets{}
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), secrets, a.Clock(), a.IDGen())
	as.SetMFACipher(cipher)
	user, raw, e := as.SetupAdmin(ctx, "AccountAdmin", "AccountRouterFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	store := postgres.NewAccountManagementStore(a.DBPool(), a.IDGen())
	accounts := auth.NewAccountManagementService(store, secrets, a.Clock(), a.IDGen(), cipher)
	mfa := auth.NewMFAService(store, secrets, cipher, a.Clock(), a.IDGen())
	h := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAccountManagementService(accounts)
	h.SetMFAService(mfa)
	router := httpapi.NewRouter(a, h, as, nil)
	send := func(method, path, key, body, credential, origin string, bearer bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if credential != "" {
			if bearer {
				r.Header.Set("Authorization", "Bearer "+credential)
			} else {
				r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: credential})
			}
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/accounts", "/api/v1/auth/sessions", "/api/v1/auth/mfa", "/api/v1/auth/security/commands/missing"} {
		w := send("GET", path, "", "", "", "", false)
		if w.Code != 401 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("anonymous guard %s status=%d cache=%q", path, w.Code, w.Header().Get("Cache-Control"))
		}
		w = send("GET", path, "", "", raw, "", true)
		if w.Code != 403 {
			t.Fatal("bearer session admitted to cookie-only route", path, w.Code)
		}
	}
	body := `{"username":"Family","password":"AccountMemberFixture123!","role":"member"}`
	if w := send("POST", "/api/v1/accounts", "cross-origin", body, raw, "https://foreign.example", false); w.Code != 403 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("CSRF admission", w.Code)
	}
	for i, bad := range []string{
		`{"username":"Family","password":"fixture123","role":"member","role":"admin"}`,
		`{"username":"Family","password":"fixture123","role":"member","r\u006fle":"admin"}`,
		`{"username":"Family","password":"fixture123","role":"member","is_active":true}`,
		`{"username":"Family","password":"fixture123","Role":"admin"}`,
		`{"username":"Family","password":"fixture123","role":null}`,
		`{"username":"Family","password":"fixture123"}`,
		`{"username":"Family","password":"fixture123","role":"member"} {}`,
		`{"username":[],"password":"fixture123","role":"member"}`,
	} {
		w := send("POST", "/api/v1/accounts", string(rune('a'+i)), bad, raw, "http://example.com", false)
		if w.Code != 400 {
			t.Fatalf("strict JSON variant %d status %d", i, w.Code)
		}
	}
	w := send("POST", "/api/v1/accounts", "create-family", body, raw, "http://example.com", false)
	if w.Code != 200 {
		t.Fatal("create account status", w.Code)
	}
	var created ports.AccountCommandResult
	if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil || created.User == nil {
		t.Fatal("account receipt", e)
	}
	w = send("GET", "/api/v1/auth/security/commands/create-family", "", "", raw, "", false)
	if w.Code != 200 || strings.Contains(w.Body.String(), "AccountMemberFixture123!") {
		t.Fatal("unsafe receipt", w.Code)
	}
	family, familyRaw, e := as.Login(ctx, "Family", "AccountMemberFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	if w = send("GET", "/api/v1/accounts", "", "", familyRaw, "", false); w.Code != 403 {
		t.Fatal("member listed accounts", w.Code)
	}
	if w = send("GET", "/api/v1/auth/security/commands/create-family", "", "", familyRaw, "", false); w.Code != 404 {
		t.Fatal("cross owner receipt", w.Code)
	}
	if w = send("PUT", "/api/v1/accounts/"+user.ID.String(), "last-admin", `{"role":"member","is_active":true}`, raw, "http://example.com", false); w.Code != 409 || !strings.Contains(w.Body.String(), "LAST_ACTIVE_ADMIN") {
		t.Fatal("last administrator guard", w.Code)
	}
	if w = send("POST", "/api/v1/auth/password", "wrong-password", `{"current_password":"wrong","new_password":"NewRouterFixture123!"}`, raw, "http://example.com", false); w.Code != 400 || !strings.Contains(w.Body.String(), "REAUTHENTICATION_FAILED") {
		t.Fatal("reauth HTTP contract", w.Code)
	}
	if _, e = as.AuthenticateToken(ctx, raw); e != nil {
		t.Fatal("wrong password revoked current session", e)
	}
	if w = send("PUT", "/api/v1/accounts/"+family.ID.String(), "disable", `{"role":"member","is_active":false}`, raw, "http://example.com", false); w.Code != 200 {
		t.Fatal("disable", w.Code)
	}
	if w = send("GET", "/api/v1/auth/sessions", "", "", familyRaw, "", false); w.Code != 401 {
		t.Fatal("disabled current session", w.Code)
	}
}
