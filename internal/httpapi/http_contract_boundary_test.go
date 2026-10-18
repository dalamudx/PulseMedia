package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
)

func TestBareAndUnknownAPIPrefixAlwaysReturnsJSON(t *testing.T) {
	_, router := setupTestApp(t)
	for _, path := range []string{"/api", "/api/", "/api/v1", "/api/v999/missing", "/api/missing?apikey=synthetic-query-secret"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			var body httpapi.ErrorResponse
			if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Code != "NOT_FOUND" {
				t.Fatalf("API fell back or wrong error: status=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
			}
			if body.RequestId == "" || body.RequestId != rec.Header().Get("X-Request-ID") || strings.Contains(rec.Body.String(), "synthetic-query-secret") {
				t.Fatal("API error correlation or query sanitation failed")
			}
		})
	}
}

func TestRequestIDRejectsUnboundedOrUnsafeClientInput(t *testing.T) {
	_, router := setupTestApp(t)
	for _, id := range []string{strings.Repeat("x", 129), "https://downloads.invalid/?apikey=synthetic-header-secret", "two words", "path/segment", "line\r\ninjection", "nonascii-é"} {
		for _, path := range []string{"/healthz", "/api/missing"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-Request-ID", id)
			router.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Request-ID")
			if got == "" || got == id || len(got) > 128 || strings.Contains(rec.Body.String(), "synthetic-header-secret") {
				t.Fatal("unsafe request ID reflected")
			}
			if path == "/api/missing" {
				var body httpapi.ErrorResponse
				if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.RequestId != got {
					t.Fatal("replacement ID not used by error")
				}
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Add("X-Request-ID", "one")
	req.Header.Add("X-Request-ID", "two")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") == "one" {
		t.Fatal("ambiguous request ID accepted")
	}
	for _, id := range []string{"custom-req-id-1234", "550e8400-e29b-41d4-a716-446655440000", "a.b_c-9", strings.Repeat("x", 128)} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Request-ID", id)
		router.ServeHTTP(rec, req)
		if rec.Header().Get("X-Request-ID") != id {
			t.Fatal("valid correlation ID changed")
		}
	}
}
