package ranking

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time                  { return c.now }
func (c *fakeClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

type fakeProvider struct {
	calls atomic.Int32
	list  string
	start int
	count int
	items []ports.DoubanRankingItem
	err   error
}

func (p *fakeProvider) GetRanking(_ context.Context, list string, start, count int) ([]ports.DoubanRankingItem, error) {
	p.calls.Add(1)
	p.list, p.start, p.count = list, start, count
	return p.items, p.err
}

type fakeMeta struct {
	ports.MetadataProvider
	calls   atomic.Int32
	running atomic.Int32
	peak    atomic.Int32
	search  func(ctx context.Context, query, kind string, year int) ([]*ports.MovieMetadata, error)
}

func (m *fakeMeta) SearchMedia(ctx context.Context, query, kind string, year int) ([]*ports.MovieMetadata, error) {
	m.calls.Add(1)
	n := m.running.Add(1)
	defer m.running.Add(-1)
	for {
		p := m.peak.Load()
		if n <= p || m.peak.CompareAndSwap(p, n) {
			break
		}
	}
	return m.search(ctx, query, kind, year)
}

func movie(id int64, title, original string, year int) *ports.MovieMetadata {
	return &ports.MovieMetadata{TMDBID: id, MediaType: "movie", Title: title, OriginalTitle: original, Year: year}
}

func douban(id, title, original string, year int) ports.DoubanRankingItem {
	return ports.DoubanRankingItem{DoubanID: id, Title: title, OriginalTitle: original, Year: year, MediaType: "movie"}
}

func TestMatchRequiresUniqueExactCandidate(t *testing.T) {
	item := douban("1", "我不是药神", "Dying to Survive", 2018)
	tv := movie(9, "我不是药神", "", 2018)
	tv.MediaType = "tv"
	cases := map[string]struct {
		found []*ports.MovieMetadata
		want  int64
	}{
		"exact":                {[]*ports.MovieMetadata{movie(5, "我不是药神", "", 2018)}, 5},
		"original title":       {[]*ports.MovieMetadata{movie(5, "Other", "Dying to Survive", 2018)}, 5},
		"punctuation/case":     {[]*ports.MovieMetadata{movie(5, "dying to survive!", "", 2018)}, 5},
		"same id twice":        {[]*ports.MovieMetadata{movie(5, "我不是药神", "", 2018), movie(5, "Dying to Survive", "", 2018)}, 5},
		"ambiguous":            {[]*ports.MovieMetadata{movie(5, "我不是药神", "", 2018), movie(6, "我不是药神", "", 2018)}, 0},
		"year differs":         {[]*ports.MovieMetadata{movie(5, "我不是药神", "", 2019)}, 0},
		"title differs":        {[]*ports.MovieMetadata{movie(5, "药神", "", 2018)}, 0},
		"type differs":         {[]*ports.MovieMetadata{tv}, 0},
		"invalid id":           {[]*ports.MovieMetadata{movie(0, "我不是药神", "", 2018), nil}, 0},
		"ambiguity then valid": {[]*ports.MovieMetadata{movie(5, "我不是药神", "", 2018), movie(6, "我不是药神", "", 2018), movie(7, "我不是药神", "", 2018)}, 0},
		"none":                 {nil, 0},
	}
	for name, tc := range cases {
		if got := match(item, tc.found); got != tc.want {
			t.Errorf("%s: got %d want %d", name, got, tc.want)
		}
	}
	if match(douban("2", "A", "", 0), []*ports.MovieMetadata{movie(5, "A", "", 0)}) != 0 {
		t.Error("unknown Douban year must stay unmapped")
	}
}

func newService(p *fakeProvider, m *fakeMeta, clock *fakeClock) *Service {
	var meta ports.MetadataProvider
	if m != nil {
		meta = m
	}
	return NewService(p, meta, clock)
}

func TestListPagingMappingAndOriginalTitleFallback(t *testing.T) {
	p := &fakeProvider{items: []ports.DoubanRankingItem{douban("1", "中文名", "English Name", 2020), douban("2", "歧义", "", 2020), douban("3", "无年份", "", 0)}}
	m := &fakeMeta{search: func(_ context.Context, query, kind string, year int) ([]*ports.MovieMetadata, error) {
		if kind != "movie" || year == 0 {
			t.Errorf("bad query %q %q %d", query, kind, year)
		}
		switch query {
		case "English Name":
			return []*ports.MovieMetadata{movie(11, "English Name", "", 2020)}, nil
		case "歧义":
			return []*ports.MovieMetadata{movie(21, "歧义", "", 2020), movie(22, "歧义", "", 2020)}, nil
		}
		return nil, nil
	}}
	s := newService(p, m, &fakeClock{now: time.Unix(1000, 0)})
	items, err := s.List(t.Context(), ports.DoubanListTop250, 3)
	if err != nil || len(items) != 3 || items[0].TMDBID != 11 || items[1].TMDBID != 0 || items[2].TMDBID != 0 || items[0].DoubanID != "1" {
		t.Fatalf("%v %+v", err, items)
	}
	if p.list != ports.DoubanListTop250 || p.start != 40 || p.count != 20 {
		t.Fatalf("window %s %d %d", p.list, p.start, p.count)
	}
	if m.calls.Load() != 3 { // item 3 has no year and costs no lookup; item 1 needs two
		t.Fatalf("lookups %d", m.calls.Load())
	}
}

func TestListRejectsBadListAndPageWithoutIO(t *testing.T) {
	p := &fakeProvider{}
	m := &fakeMeta{search: func(context.Context, string, string, int) ([]*ports.MovieMetadata, error) { return nil, nil }}
	s := newService(p, m, &fakeClock{})
	for _, tc := range []struct {
		list string
		page int
	}{{"nope", 1}, {"", 1}, {ports.DoubanListTop250, 0}, {ports.DoubanListTop250, 14}, {ports.DoubanListHotMovie, 2}, {ports.DoubanListWeeklyBest, 2}, {ports.DoubanListHotTV, -1}} {
		if _, err := s.List(t.Context(), tc.list, tc.page); !errors.Is(err, ports.ErrRejected) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	if _, err := s.List(t.Context(), ports.DoubanListTop250, 13); err != nil {
		t.Fatalf("last page: %v", err)
	}
	if p.calls.Load() != 1 || m.calls.Load() != 0 {
		t.Fatalf("I/O for rejected input: %d %d", p.calls.Load(), m.calls.Load())
	}
}

func TestCacheTTLAndIsolation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	p := &fakeProvider{items: []ports.DoubanRankingItem{douban("1", "A", "", 2020)}}
	m := &fakeMeta{search: func(context.Context, string, string, int) ([]*ports.MovieMetadata, error) {
		return []*ports.MovieMetadata{movie(5, "A", "", 2020)}, nil
	}}
	s := newService(p, m, clock)
	first, err := s.List(t.Context(), ports.DoubanListHotMovie, 1)
	if err != nil || first[0].TMDBID != 5 {
		t.Fatal(first, err)
	}
	first[0].TMDBID = 999 // callers cannot poison the cache
	clock.now = clock.now.Add(cacheTTL - time.Second)
	second, err := s.List(t.Context(), ports.DoubanListHotMovie, 1)
	if err != nil || second[0].TMDBID != 5 || p.calls.Load() != 1 {
		t.Fatalf("%v %+v calls=%d", err, second, p.calls.Load())
	}
	clock.now = clock.now.Add(2 * time.Second)
	if _, err = s.List(t.Context(), ports.DoubanListHotMovie, 1); err != nil || p.calls.Load() != 2 {
		t.Fatalf("expired entry reused: %v %d", err, p.calls.Load())
	}
	// Distinct lists and pages never share an entry.
	if _, err = s.List(t.Context(), ports.DoubanListHotTV, 1); err != nil || p.calls.Load() != 3 {
		t.Fatalf("%v %d", err, p.calls.Load())
	}
}

func TestFailedOrPartialResultsAreNotCached(t *testing.T) {
	p := &fakeProvider{items: []ports.DoubanRankingItem{douban("1", "A", "", 2020), douban("2", "B", "", 2020)}}
	var fail atomic.Bool
	fail.Store(true)
	m := &fakeMeta{search: func(_ context.Context, query, _ string, _ int) ([]*ports.MovieMetadata, error) {
		if query == "B" && fail.Load() {
			return nil, ports.ErrUnavailable
		}
		return []*ports.MovieMetadata{movie(int64(query[0]), query, "", 2020)}, nil
	}}
	s := newService(p, m, &fakeClock{now: time.Unix(1, 0)})
	items, err := s.List(t.Context(), ports.DoubanListHotMovie, 1)
	if err != nil || items[0].TMDBID != int64('A') || items[1].TMDBID != 0 {
		t.Fatalf("degraded item must remain readable: %v %+v", err, items)
	}
	fail.Store(false)
	items, err = s.List(t.Context(), ports.DoubanListHotMovie, 1)
	if err != nil || items[1].TMDBID != int64('B') || p.calls.Load() != 2 {
		t.Fatalf("partial result was cached: %v %+v calls=%d", err, items, p.calls.Load())
	}
	// Provider failures are returned and never cached.
	p.err = ports.ErrUnavailable
	s2 := newService(p, m, &fakeClock{now: time.Unix(1, 0)})
	if _, err = s2.List(t.Context(), ports.DoubanListHotTV, 1); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	p.err = nil
	if _, err = s2.List(t.Context(), ports.DoubanListHotTV, 1); err != nil {
		t.Fatal(err)
	}
}

func TestWithoutMetadataProviderItemsAreUnmapped(t *testing.T) {
	p := &fakeProvider{items: []ports.DoubanRankingItem{douban("1", "A", "", 2020)}}
	s := newService(p, nil, &fakeClock{now: time.Unix(1, 0)})
	items, err := s.List(t.Context(), ports.DoubanListHotMovie, 1)
	if err != nil || len(items) != 1 || items[0].TMDBID != 0 {
		t.Fatalf("%v %+v", err, items)
	}
	if _, err = s.List(t.Context(), ports.DoubanListHotMovie, 1); err != nil || p.calls.Load() != 2 {
		t.Fatalf("unmapped result cached: %v %d", err, p.calls.Load())
	}
}

func TestLookupConcurrencyIsBoundedAndWaited(t *testing.T) {
	var rows []ports.DoubanRankingItem
	for i := 0; i < 20; i++ {
		rows = append(rows, douban(string(rune('a'+i)), string(rune('A'+i)), "", 2020))
	}
	p := &fakeProvider{items: rows}
	var wg sync.WaitGroup
	m := &fakeMeta{search: func(context.Context, string, string, int) ([]*ports.MovieMetadata, error) {
		time.Sleep(10 * time.Millisecond)
		return nil, nil
	}}
	s := newService(p, m, &fakeClock{now: time.Unix(1, 0)})
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = s.List(t.Context(), ports.DoubanListHotMovie, 1) }()
	wg.Wait()
	if m.running.Load() != 0 {
		t.Fatal("worker outlived List")
	}
	if peak := m.peak.Load(); peak < 2 || peak > lookupWorkers {
		t.Fatalf("peak concurrency %d", peak)
	}
}

func TestCancellationWaitsForLookupsAndReturnsContextError(t *testing.T) {
	p := &fakeProvider{items: []ports.DoubanRankingItem{douban("1", "A", "", 2020), douban("2", "B", "", 2020)}}
	started := make(chan struct{}, 4)
	m := &fakeMeta{search: func(ctx context.Context, _, _ string, _ int) ([]*ports.MovieMetadata, error) {
		started <- struct{}{}
		<-ctx.Done()
		time.Sleep(30 * time.Millisecond) // transport returns late
		return nil, ctx.Err()
	}}
	s := newService(p, m, &fakeClock{now: time.Unix(1, 0)})
	ctx, cancel := context.WithCancel(t.Context())
	go func() { <-started; cancel() }()
	_, err := s.List(ctx, ports.DoubanListHotMovie, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if m.running.Load() != 0 {
		t.Fatal("List returned while a lookup was still running")
	}
	if _, ok := s.cached(ports.DoubanListHotMovie + ":1"); ok {
		t.Fatal("canceled result cached")
	}
}
