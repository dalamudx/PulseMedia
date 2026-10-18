package tmdb_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
)

func TestTMDBPhysicalRequestsLimitedToFour(t *testing.T) {
	var active, peak atomic.Int32
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := active.Add(1)
		defer active.Add(-1)
		for {
			previous := peak.Load()
			if count <= previous || peak.CompareAndSwap(previous, count) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		_, _ = fmt.Fprint(w, `{"results":[]}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client := tmdb.NewClient("synthetic-key", tmdb.WithBaseURL(server.URL))
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := client.SearchMovies(ctx, "independent", 0); done <- err }()
	}
	defer func() {
		unblock()
		for i := 0; i < 8; i++ {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("four requests never started")
		}
	}
	select {
	case <-entered:
		t.Fatal("fifth physical request bypassed the metadata budget")
	case <-time.After(120 * time.Millisecond):
	}
	unblock()
	if peak.Load() > 4 {
		t.Fatal("metadata physical concurrency exceeded four", peak.Load())
	}
}
