package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSeedingInspectionRejectsNoncanonicalUUIDAndHeaders(t *testing.T) {
	const id = "12345678-1234-4234-8234-123456789abc"
	const base = "/api/v1/operations/"
	const suffix = "/seeding-observations"
	for _, target := range []string{
		base + "invalid" + suffix, base + "00000000-0000-0000-0000-000000000000" + suffix,
		base + strings.ToUpper(id) + suffix, base + strings.ReplaceAll(id, "-", "") + suffix,
		base + "12345678-1234-4234-8234-123456789ab%63" + suffix,
	} {
		h := StrictSeedingInspectionRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("noncanonical operation dispatched") }))
		req := httptest.NewRequest("POST", target, nil)
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: "fixture"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 400 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("noncanonical UUID admitted", w.Code)
		}
	}
	for _, mode := range []string{"API key", "empty cookie", "duplicate empty cookie", "wrong MIME"} {
		t.Run(mode, func(t *testing.T) {
			h := StrictSeedingInspectionRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid credential or MIME dispatched") }))
			req := httptest.NewRequest("POST", base+id+suffix, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			cookie := "fixture"
			if mode == "empty cookie" {
				cookie = ""
			}
			req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: cookie})
			expected := 401
			switch mode {
			case "API key":
				req.Header.Set("X-API-Key", "private")
				expected = 403
			case "duplicate empty cookie":
				req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: ""})
			case "wrong MIME":
				req.Header.Set("Content-Type", "text/plain")
				expected = 400
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != expected || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code)
			}
		})
	}
}
