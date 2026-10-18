package search_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type failingIndexer struct{}

const sensitiveIndexerError = "request failed https://tracker.example/dl?passkey=secret-indexer-test Cookie=session-secret"

func (*failingIndexer) Search(context.Context, string, []int32, int32, int32) ([]*ports.ReleaseItem, error) {
	return nil, errors.New(sensitiveIndexerError)
}

func (*failingIndexer) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrUnavailable
}

func TestAggregateSearchFailureEmptyPartialAndCancellation(t *testing.T) {
	svc, pool, _, _, idx := setupSearchTest(t)
	ctx := context.Background()

	// Existing candidates must not turn a failed live search into success.
	svc.RegisterClient(idx, &mockIndexerClient{releases: []*ports.ReleaseItem{{
		Title: "Contract Movie", GUID: "cached-contract", DownloadURL: "https://example.invalid/download", Seeders: 1,
	}}})
	if _, err := svc.ExecuteSearch(ctx, idx, "cached", 0); err != nil {
		t.Fatal(err)
	}
	svc.RegisterClient(idx, &failingIndexer{})
	if _, err := svc.ExecuteSearch(ctx, idx, "direct-failure", 0); !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("direct search must return the sanitized ports contract: %v", err)
	}
	if rows, err := svc.AggregateSearch(ctx, "Contract Movie"); !errors.Is(err, ports.ErrUnavailable) || len(rows) != 0 {
		t.Fatalf("all failed with cached candidates: rows=%d err=%v", len(rows), err)
	}
	var stored string
	if err := pool.QueryRow(ctx, "SELECT error FROM indexer_query WHERE state = 'failed' LIMIT 1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "indexer search failed" {
		t.Fatalf("unsafe stored error: %q", stored)
	}

	// A genuinely successful empty response is distinct from all failed.
	svc.RegisterClient(idx, &mockIndexerClient{})
	if rows, err := svc.AggregateSearch(ctx, "No Such Movie"); err != nil || len(rows) != 0 {
		t.Fatalf("successful empty response: rows=%d err=%v", len(rows), err)
	}

	if rows, err := svc.AggregateSearch(ctx, "Contract Movie"); err != nil || len(rows) != 0 {
		t.Fatalf("successful empty query leaked historical candidates: %d %v", len(rows), err)
	}

	other, err := svc.CreateIndexer(ctx, "Failed secondary", "https://secondary.invalid", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.RegisterClient(other.ID, &failingIndexer{})
	svc.RegisterClient(idx, &mockIndexerClient{releases: []*ports.ReleaseItem{{Title: "Contract Movie", GUID: "fresh-partial", DownloadURL: "https://fixture.invalid/current"}}})
	if rows, partial, err := svc.AggregateSearchDetailed(ctx, "Contract Movie"); err != nil || !partial || len(rows) != 1 || rows[0].Guid != "fresh-partial" {
		t.Fatalf("healthy endpoint plus failed endpoint: rows=%d err=%v", len(rows), err)
	}

	svc.RegisterClient(idx, &mockIndexerClient{})
	if rows, err := svc.AggregateSearch(ctx, "Mixed empty and failed"); !errors.Is(err, ports.ErrUnavailable) || len(rows) != 0 {
		t.Fatalf("partial absence falsely complete: %d %v", len(rows), err)
	}
	svc.SetClientFactory(func(string, string) ports.IndexerClient { return nil })
	third, err := svc.CreateIndexer(ctx, "Constructor unavailable", "https://constructor.invalid", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.RegisterClient(other.ID, &mockIndexerClient{})
	if rows, err := svc.AggregateSearch(ctx, "Constructor plus empty"); !errors.Is(err, ports.ErrUnavailable) || len(rows) != 0 {
		t.Fatalf("unqueried source masked by empty success: %d %v (%s)", len(rows), err, third.ID)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.AggregateSearch(canceled, "Contract Movie"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel must propagate: %v", err)
	}

	ok, _, message, err := svc.TestIndexer(ctx, other.ID)
	if ok || !errors.Is(err, ports.ErrUnavailable) || strings.Contains(message, "secret") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("probe must not expose adapter credentials: ok=%v message=%s err=%v", ok, message, err)
	}
}

func TestSearchClientRegistryConcurrentAccess(t *testing.T) {
	svc, _, _, _, idx := setupSearchTest(t)
	client := &mockIndexerClient{}
	svc.RegisterClient(idx, client)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				svc.RegisterClient(idx, client)
				svc.SetClientFactory(func(string, string) ports.IndexerClient { return client })
				if _, err := svc.AggregateSearch(context.Background(), fmt.Sprintf("concurrent-%d-%d", n, i)); err != nil && !errors.Is(err, ports.ErrUnavailable) {
					t.Errorf("concurrent search failed: %v", err)
					return
				}
			}
		}(n)
	}
	wg.Wait()
}
