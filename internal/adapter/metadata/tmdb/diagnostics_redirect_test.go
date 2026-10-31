package tmdb

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMetadataDiagnosticNeverFollowsRedirectOrLeaksCredentials(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		t.Error("unapproved redirect reached private third host")
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.URL.RawQuery != "" {
			t.Error("redirect leaked credentials or Referer")
		}
		_, _ = io.WriteString(w, `{"id":550,"title":"unexpected"}`)
	}))
	defer destination.Close()
	for _, target := range []ports.MetadataDiagnosticTarget{ports.DiagnosticTMDBAPI, ports.DiagnosticTMDBImage} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			var originCalls atomic.Int32
			var inheritedRedirects atomic.Int32
			client := diagnosticHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				originCalls.Add(1)
				w.Header().Set("Location", destination.URL+"/private?api_key=server-secret")
				w.WriteHeader(status)
			}, time.Second, nil)
			client.CheckRedirect = func(*http.Request, []*http.Request) error { inheritedRedirects.Add(1); return nil }
			result := NewClient("credential-key", WithHTTPClient(client)).ProbeDiagnostic(context.Background(), target)
			if result.Category != "host_response" || !result.HostResponded || result.ImageRetrieved || originCalls.Load() != 1 || destinationCalls.Load() != 0 || inheritedRedirects.Load() != 0 {
				t.Fatalf("redirect target=%s status=%d result=%+v origin=%d destination=%d inherited=%d", target, status, result, originCalls.Load(), destinationCalls.Load(), inheritedRedirects.Load())
			}
		}
	}
}
