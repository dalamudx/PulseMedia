package httpapi_test

import (
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStrictLoginSecurityActualRouterPG(t *testing.T) {
	a, as, _ := setupAuthHTTP(t)
	const fixturePassword = "OwnedLoginFixture123!"
	if _, _, e := as.SetupAdmin(t.Context(), "StrictUser", fixturePassword); e != nil {
		t.Fatal(e)
	}
	h := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(a, h, as, nil)
	c := a.Clock().(*clock.MockClock)
	prefix := `{"username":"StrictUser","password":"` + fixturePassword + `"`
	for _, body := range []string{
		prefix + `,"otp":"123456","otp":"654321"}`, prefix + `,"otp":"123456","o\u0074p":"654321"}`, prefix + `,"OTP":"123456"}`, prefix + `,"Username":"other"}`, prefix + `,"scopes":["system"]}`,
		prefix + `,"otp":null}`, prefix + `,"recovery_code":null}`, prefix + `,"otp":123456}`, prefix + `,"otp":""}`, prefix + `,"otp":"12345"}`, prefix + `,"otp":"abcdef"}`, prefix + `,"recovery_code":""}`, prefix + `,"recovery_code":"` + strings.Repeat("A", 65) + `"}`,
		prefix + `,"otp":"123456","recovery_code":"one"}`, prefix + `} {}`, `{"username":"StrictUser"}`, `{"username":null,"password":"value"}`, `[]`,
	} {
		c.Advance(6 * time.Second)
		r := httptest.NewRequest("POST", "http://example.com/api/v1/auth/login", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 400 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("strict login guard status=%d", w.Code)
		}
	}
	for _, suffix := range []string{`}`, `,"otp":"123456"}`, `,"recovery_code":"UNUSED-OPTIONAL-CODE"}`} {
		c.Advance(6 * time.Second)
		r := httptest.NewRequest("POST", "http://example.com/api/v1/auth/login", strings.NewReader(prefix+suffix))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal("valid public login rejected", w.Code)
		}
	}
	c.Advance(6 * time.Second)
	r := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/auth/login", strings.NewReader(prefix+`}`))
	r.Header.Add("Content-Type", "application/json")
	r.Header.Add("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("duplicate content type accepted", w.Code)
	}
}
