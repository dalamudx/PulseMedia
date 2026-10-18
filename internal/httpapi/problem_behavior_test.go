package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type unavailableReadinessServer struct{ httpapi.StrictServerInterface }

func (unavailableReadinessServer) GetReadyz(context.Context, httpapi.GetReadyzRequestObject) (httpapi.GetReadyzResponseObject, error) {
	checks := map[string]bool{"database": false}
	return httpapi.GetReadyz503JSONResponse{Ready: false, Checks: &checks}, nil
}

func TestProblemDetailsHealthMethodsAndReadiness(t *testing.T) {
	a, router := setupTestApp(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			t.Run(path+method, func(t *testing.T) {
				rec := httptest.NewRecorder()
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("X-Request-ID", "health-method-123")
				router.ServeHTTP(rec, r)
				assertProblem(t, rec, 405, "METHOD_NOT_ALLOWED", "health-method-123")
				if rec.Header().Get("Allow") != "GET, HEAD" {
					t.Fatal("wrong Allow")
				}
			})
		}
	}
	rec := httptest.NewRecorder()
	httpapi.NewRouter(a, unavailableReadinessServer{}, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var health struct {
		Ready  bool            `json:"ready"`
		Checks map[string]bool `json:"checks"`
	}
	if rec.Code != 503 || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") || json.Unmarshal(rec.Body.Bytes(), &health) != nil || health.Ready || health.Checks == nil {
		t.Fatalf("readiness report changed: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/api/no-such-endpoint", nil))
	if rec.Code != 404 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatal("HEAD error wrote body or wrong media")
	}
}

func TestProblemDetailsPrivateFallbackAndFieldErrors(t *testing.T) {
	for _, kind := range []string{"text", "oversized", "malformed", "fields"} {
		t.Run(kind, func(t *testing.T) {
			h := httpapi.RequestIDMiddleware(nil)(httpapi.ProblemResponseMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "1234")
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(422)
				switch kind {
				case "text":
					_, _ = fmt.Fprint(w, "database exception: sensitive-download-passkey")
				case "oversized":
					_, _ = fmt.Fprint(w, strings.Repeat("sensitive-download-passkey", 4000))
				case "malformed":
					_, _ = fmt.Fprint(w, `{"code":"BAD_DATA","message":"safe"} trailing sensitive-download-passkey`)
				case "fields":
					_, _ = fmt.Fprint(w, `{"code":"INVALID_FIELD","message":"invalid fields","request_id":"wrong-old-id","details":{"version":"required"},"status":200,"type":"https://private.invalid/?passkey=sensitive-download-passkey","cause":"sensitive-download-passkey"}`)
				}
			})))
			r := httptest.NewRequest(http.MethodGet, "/api/test?passkey=synthetic-passkey", nil)
			r.Header.Set("X-Request-ID", "fallback-123")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			code := "HTTP_ERROR"
			if kind == "fields" {
				code = "INVALID_FIELD"
			}
			assertProblem(t, rec, 422, code, "fallback-123")
			if rec.Header().Get("Content-Length") != "" || rec.Header().Get("Retry-After") != "7" {
				t.Fatal("wrong retained/removed headers")
			}
			if kind == "fields" {
				var p map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if p["details"].(map[string]any)["version"] != "required" {
					t.Fatal("field errors lost")
				}
			}
		})
	}
}

func TestProblemDetailsDoesNotDelayStreaming(t *testing.T) {
	done := make(chan struct{})
	h := httpapi.ProblemResponseMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: connected\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	server := httptest.NewServer(h)
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "event: connected\n" || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE delayed/corrupted: %q %v", line, err)
	}
	_ = resp.Body.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream handler did not stop")
	}
	payload := bytes.Repeat([]byte("complete-file-data"), 6000)
	rec := httptest.NewRecorder()
	httpapi.ProblemResponseMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, bytes.NewReader(payload))
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/file", nil))
	if rec.Code != 200 || !bytes.Equal(payload, rec.Body.Bytes()) {
		t.Fatal("successful large body truncated")
	}
}
