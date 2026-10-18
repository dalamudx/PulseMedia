package tmdb_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type metadataBudgetTransport func(*http.Request) (*http.Response, error)

func (f metadataBudgetTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type stalledMetadataClose struct {
	io.Reader
	entered chan<- struct{}
	release <-chan struct{}
}

func (b *stalledMetadataClose) Close() error { b.entered <- struct{}{}; <-b.release; return nil }

func TestTMDBBudgetHeldThroughCancelledResponseBodyClose(t *testing.T) {
	clk := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	gate := budget.NewMetadataBudget(clk)
	entered := make(chan struct{}, 4)
	finish := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(finish) }) }
	var calls atomic.Int32
	transport := metadataBudgetTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &stalledMetadataClose{Reader: strings.NewReader(`{"results":[]}`), entered: entered, release: finish}, Request: r}, nil
	})
	clients := []*tmdb.Client{
		tmdb.NewClient("synthetic-one", tmdb.WithRequestBudget(gate), tmdb.WithHTTPClient(&http.Client{Transport: transport})),
		tmdb.NewClient("synthetic-two", tmdb.WithRequestBudget(gate), tmdb.WithHTTPClient(&http.Client{Transport: transport})),
	}
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		index := i
		go func() {
			ctx := context.Background()
			if index == 0 {
				ctx = owner
			}
			_, e := clients[index%2].SearchMovies(ctx, "physical", 0)
			done <- e
		}()
	}
	defer func() {
		unblock()
		for i := 0; i < 4; i++ {
			if e := <-done; e != nil && !errors.Is(e, context.Canceled) {
				t.Error(e)
			}
		}
	}()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-timeout.C:
			t.Fatal("body close never started")
		}
	}
	cancel()
	clk.Advance(time.Second)
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if _, e := clients[1].SearchMovies(ctx, "must-wait", 0); !errors.Is(e, ports.ErrTimeout) {
		t.Fatal("cancel released physical slot before close returned", e)
	}
	if calls.Load() != 4 {
		t.Fatal("fifth transport call started", calls.Load())
	}
	unblock()
}

func TestTMDB429BudgetSharedAcrossCredentialClients(t *testing.T) {
	clk := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	gate := budget.NewMetadataBudget(clk)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "12")
			w.WriteHeader(429)
			return
		}
		_, _ = fmt.Fprint(w, `{"results":[]}`)
	}))
	defer server.Close()
	first := tmdb.NewClient("synthetic-one", tmdb.WithBaseURL(server.URL), tmdb.WithRequestBudget(gate))
	peer := tmdb.NewClient("synthetic-two", tmdb.WithBaseURL(server.URL), tmdb.WithRequestBudget(gate))
	if _, e := first.SearchMovies(context.Background(), "first", 0); e == nil {
		t.Fatal("429 accepted")
	}
	if _, e := peer.SearchMovies(context.Background(), "peer", 0); e == nil {
		t.Fatal("peer ignored cooldown")
	} else if rate, ok := ports.IsRateLimited(e); !ok || rate.RetryAfter != 12*time.Second {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("peer made a remote request during cooldown")
	}
	clk.Advance(12 * time.Second)
	if _, e := peer.SearchMovies(context.Background(), "recovered", 0); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatal("budget did not recover")
	}
}

func TestTMDBAdmissionUsesExistingHTTPTimeout(t *testing.T) {
	gate := budget.NewMetadataBudget(clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)))
	for i := 0; i < 4; i++ {
		release, e := gate.Acquire(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer release()
	}
	var calls atomic.Int32
	transport := metadataBudgetTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("synthetic unexpected transport call")
	})
	client := tmdb.NewClient("synthetic", tmdb.WithRequestBudget(gate), tmdb.WithHTTPClient(&http.Client{Timeout: 20 * time.Millisecond, Transport: transport}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := client.SearchMovies(ctx, "bounded-wait", 0); done <- e }()
	select {
	case e := <-done:
		if !errors.Is(e, ports.ErrTimeout) {
			t.Fatal("unexpected timeout contract", e)
		}
	case <-time.After(200 * time.Millisecond):
		cancel()
		e := <-done
		t.Fatal("HTTP timeout did not include admission wait", e)
	}
	if calls.Load() != 0 {
		t.Fatal("timed-out waiter performed physical request")
	}
}
