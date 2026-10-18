package httpapi_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code, requestID string) {
	t.Helper()
	media, _, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
	if err != nil || media != "application/problem+json" {
		t.Fatalf("wrong error media type: %q", rec.Header().Get("Content-Type"))
	}
	var p map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if rec.Code != status || p["status"] != float64(status) || p["type"] != "about:blank" || p["title"] != http.StatusText(status) || p["code"] != code || p["request_id"] != requestID || rec.Header().Get("X-Request-ID") != requestID {
		t.Fatalf("wrong problem contract: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if p["detail"] != p["message"] || p["detail"] == "" {
		t.Fatalf("legacy message/detail mismatch: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "synthetic-passkey") || strings.Contains(rec.Body.String(), "sensitive-download-passkey") {
		t.Fatal("private data leaked")
	}
	if _, ok := p["instance"]; ok {
		t.Fatal("raw request URI must not enter problem")
	}
}
func TestProblemDetailsPublicRoutes(t *testing.T) {
	a, router := setupTestApp(t)
	failing := httpapi.NewRouter(a, failingHealthServer{}, nil, nil)
	for _, tc := range []struct {
		path    string
		status  int
		code    string
		handler http.Handler
	}{
		{"/api", 404, "NOT_FOUND", router},
		{"/api/v1/no-such-endpoint", 404, "NOT_FOUND", router},
		{"/api/v1/auth/me", 401, "UNAUTHORIZED", router},
		{"/api/v1/operations/not-a-uuid", 400, "INVALID_REQUEST", router},
		{"/healthz", 500, "INTERNAL_ERROR", failing},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path+"?passkey=synthetic-passkey", nil)
			r.Header.Set("X-Request-ID", "rfc-contract-123")
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, r)
			assertProblem(t, rec, tc.status, tc.code, "rfc-contract-123")
		})
	}
}
func TestProblemDetailsDirectErrorWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "direct-123")
	httpapi.WriteJSONError(rec, 409, "VERSION_CONFLICT", "configuration changed", "direct-123")
	assertProblem(t, rec, 409, "VERSION_CONFLICT", "direct-123")
}
