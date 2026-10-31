package tmdb

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMetadataDiagnosticBearerAndPreCanceledRequest(t *testing.T) {
	key := "ey" + strings.Repeat("credential", 8)
	var calls atomic.Int32
	client := diagnosticHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+key || r.URL.Query().Has("api_key") {
			t.Error("production bearer representation changed")
		}
		_, _ = io.WriteString(w, `{"id":550,"title":"Fight Club"}`)
	}, time.Second, nil)
	c := NewClient(key, WithHTTPClient(client))
	if result := c.ProbeDiagnostic(context.Background(), ports.DiagnosticTMDBAPI); result.Category != "available" {
		t.Fatalf("bearer probe=%+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := c.ProbeDiagnostic(ctx, ports.DiagnosticTMDBAPI); result.Category != "cancelled" || result.HostResponded {
		t.Fatalf("cancelled=%+v", result)
	}
	if calls.Load() != 1 {
		t.Fatal("pre-canceled request performed network I/O")
	}
	if result := c.ProbeDiagnostic(context.Background(), "https://private.invalid"); result.Category != "unavailable" || result.HostResponded {
		t.Fatalf("arbitrary target=%+v", result)
	}
	if calls.Load() != 1 {
		t.Fatal("arbitrary target performed network I/O")
	}
}
