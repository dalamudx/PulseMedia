package tmdb

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type diagnosticRoundTrip func(*http.Request) (*http.Response, error)

func (f diagnosticRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type diagnosticPhysicalBudget struct{ active atomic.Int32 }

func (b *diagnosticPhysicalBudget) Acquire(context.Context) (func(), error) {
	b.active.Add(1)
	return func() { b.active.Add(-1) }, nil
}
func (b *diagnosticPhysicalBudget) RecordRateLimit(time.Duration) {}

type diagnosticBlockingClose struct {
	io.Reader
	entered, release chan struct{}
}

func (b diagnosticBlockingClose) Close() error { close(b.entered); <-b.release; return nil }
func TestMetadataDiagnosticOwnsPhysicalCloseAfterCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	b := &diagnosticPhysicalBudget{}
	client := &http.Client{Timeout: time.Second, Transport: diagnosticRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: diagnosticBlockingClose{strings.NewReader(`{"id":550,"title":"Fight Club"}`), entered, release}}, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan ports.MetadataDiagnosticResult, 1)
	go func() {
		done <- NewClient("secret", WithHTTPClient(client), WithRequestBudget(b)).ProbeDiagnostic(ctx, ports.DiagnosticTMDBAPI)
	}()
	<-entered
	cancel()
	if b.active.Load() != 1 {
		t.Error("released budget while body close still executing")
	}
	select {
	case <-done:
		t.Error("returned while physical close still executing")
	default:
	}
	close(release)
	result := <-done
	if b.active.Load() != 0 || result.Category != "cancelled" || result.ImageRetrieved {
		t.Fatalf("result=%+v active=%d", result, b.active.Load())
	}
}
func TestMetadataDiagnosticNetworkErrorsAreSafe(t *testing.T) {
	client := &http.Client{Timeout: time.Second, Transport: diagnosticRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://private.invalid?api_key=secret&token=raw")
	})}
	result := NewClient("secret", WithHTTPClient(client)).ProbeDiagnostic(context.Background(), ports.DiagnosticTMDBAPI)
	if result.Category != "unavailable" || result.HostResponded || result.ImageRetrieved {
		t.Fatalf("unsafe error result=%+v", result)
	}
}
func TestMetadataDiagnosticMissingCredentialOnlyBlocksAPI(t *testing.T) {
	source := NewDiagnosticSource(nil, func() string { return "" }, &http.Client{Timeout: time.Second}, budget.NewMetadataBudget(nil))
	session, err := source.OpenDiagnostic(context.Background())
	if err != nil || session == nil {
		t.Fatalf("image-capable snapshot unavailable: %v", err)
	}
	result := session.Probe(context.Background(), ports.DiagnosticTMDBAPI)
	if result.Category != "not_configured" || result.HostResponded {
		t.Fatalf("unconfigured API result=%+v", result)
	}
}
