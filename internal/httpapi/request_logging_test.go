package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestDiagnosticsStatusAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		status  int
		size    int
		flushed bool
	}{
		{"empty", func(http.ResponseWriter, *http.Request) {}, 200, 0, false},
		{"body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }, 200, 2, false},
		{"duplicate", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202); w.WriteHeader(500) }, 202, 0, false},
		{"stream", func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte("event"))
		}, 200, 5, true},
		{"panic", func(http.ResponseWriter, *http.Request) { panic("private-panic postgres://private-credential") }, 500, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&log, nil))
			mux := http.NewServeMux()
			mux.HandleFunc("GET /operations/{id}", tc.handler)
			handler := LoggingMiddleware(logger, func(r *http.Request) string { _, p := mux.Handler(r); return p })(RecoveryMiddleware(logger)(mux))
			req := httptest.NewRequest("GET", "/operations/private-identity?token=private-query", nil)
			req.Header.Set("Cookie", "private-cookie")
			req.Header.Set("Authorization", "Bearer private-bearer")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.status || w.Flushed != tc.flushed {
				t.Fatalf("response: %d flushed=%v", w.Code, w.Flushed)
			}
			lines := strings.Split(strings.TrimSpace(log.String()), "\n")
			var entry map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["status"] != float64(tc.status) || entry["path"] != "GET /operations/{id}" {
				t.Fatalf("diagnostics: %v", entry)
			}
			if tc.size >= 0 && entry["bytes"] != float64(tc.size) {
				t.Fatal(entry)
			}
			if strings.Contains(log.String(), "private-") {
				t.Fatal("request data reached logs")
			}
		})
	}
}

func TestRequestDiagnosticsUnknownRoutesAndMethodsAreBounded(t *testing.T) {
	var log bytes.Buffer
	mux := http.NewServeMux()
	handler := LoggingMiddleware(slog.New(slog.NewJSONHandler(&log, nil)), func(r *http.Request) string { _, p := mux.Handler(r); return p })(mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PRIVATE-METHOD", "/private-path?key=private-query", nil))
	var entry map[string]any
	if err := json.Unmarshal(log.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["path"] != "unmatched" || entry["method"] != "OTHER" || entry["status"] != float64(404) || strings.Contains(log.String(), "private") {
		t.Fatal(entry)
	}
}
