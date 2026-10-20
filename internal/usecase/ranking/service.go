// Package ranking serves external ranking lists as standard media data.
// A TMDB identity is attached only when one candidate matches exactly; an
// ambiguous or failed lookup leaves the item readable but not subscribable.
package ranking

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	pageSize      = 20
	cacheTTL      = 10 * time.Minute
	lookupWorkers = 4
)

// maxPages bounds how far each ranking may be paged. Top250 is a fixed
// 250-entry list; the weekly boards are a single short window.
var maxPages = map[string]int{
	ports.DoubanListTop250:        13,
	ports.DoubanListHotMovie:      5,
	ports.DoubanListHotTV:         5,
	ports.DoubanListWeeklyChinese: 1,
	ports.DoubanListWeeklyGlobal:  1,
}

// Item is one ranking row. TMDBID is zero when no unique match was proven.
type Item struct {
	ports.DoubanRankingItem
	TMDBID int64
}

type entry struct {
	items   []Item
	expires time.Time
}

type Service struct {
	provider ports.DoubanRankingProvider
	meta     ports.MetadataProvider
	clock    ports.Clock

	mu    sync.Mutex
	cache map[string]entry
}

// NewService takes an optional metadata provider; without one every item is
// returned unmapped.
func NewService(provider ports.DoubanRankingProvider, meta ports.MetadataProvider, clock ports.Clock) *Service {
	return &Service{provider: provider, meta: meta, clock: clock, cache: make(map[string]entry)}
}

// List returns one page. Pages are bounded per ranking by maxPages.
func (s *Service) List(ctx context.Context, list string, page int) ([]Item, error) {
	if s == nil || s.provider == nil || s.clock == nil {
		return nil, ports.ErrUnavailable
	}
	maxPage, known := maxPages[list]
	if !known {
		return nil, ports.ErrRejected
	}
	if page < 1 || page > maxPage {
		return nil, ports.ErrRejected
	}
	key := list + ":" + strconv.Itoa(page)
	if items, ok := s.cached(key); ok {
		return items, nil
	}
	ranked, err := s.provider.GetRanking(ctx, list, (page-1)*pageSize, pageSize)
	if err != nil {
		return nil, err
	}
	items, complete, err := s.resolve(ctx, ranked)
	if err != nil {
		return nil, err
	}
	if complete {
		s.store(key, items)
	}
	return append([]Item(nil), items...), nil
}

func (s *Service) cached(key string) ([]Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok || !s.clock.Now().Before(e.expires) {
		delete(s.cache, key)
		return nil, false
	}
	return append([]Item(nil), e.items...), true
}

func (s *Service) store(key string, items []Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = entry{items: append([]Item(nil), items...), expires: s.clock.Now().Add(cacheTTL)}
}

// resolve maps items with bounded concurrency and waits for every worker.
// complete is false when any lookup failed, so partial results are not cached.
func (s *Service) resolve(ctx context.Context, ranked []ports.DoubanRankingItem) ([]Item, bool, error) {
	out := make([]Item, len(ranked))
	for i := range ranked {
		out[i].DoubanRankingItem = ranked[i]
	}
	if s.meta == nil {
		return out, false, nil
	}
	var failed atomic.Bool
	var wg sync.WaitGroup
	sem := make(chan struct{}, lookupWorkers)
dispatch:
	for i := range ranked {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			id, err := s.lookup(ctx, ranked[i])
			if err != nil {
				failed.Store(true)
				return
			}
			out[i].TMDBID = id
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return out, !failed.Load(), nil
}

func (s *Service) lookup(ctx context.Context, item ports.DoubanRankingItem) (int64, error) {
	if item.Year == 0 {
		return 0, nil
	}
	queries := []string{item.Title}
	if item.OriginalTitle != "" && item.OriginalTitle != item.Title {
		queries = append(queries, item.OriginalTitle)
	}
	for _, query := range queries {
		found, err := s.meta.SearchMedia(ctx, query, item.MediaType, item.Year)
		if err != nil {
			return 0, err
		}
		if id := match(item, found); id != 0 {
			return id, nil
		}
	}
	return 0, nil
}

// match returns the TMDB ID only when exactly one distinct candidate has the
// same media type, year and a title equal to Douban's title or original title.
func match(item ports.DoubanRankingItem, candidates []*ports.MovieMetadata) int64 {
	wanted := map[string]bool{}
	for _, title := range []string{item.Title, item.OriginalTitle} {
		if n := normalize(title); n != "" {
			wanted[n] = true
		}
	}
	var found int64
	for _, c := range candidates {
		if c == nil || c.TMDBID <= 0 || c.MediaType != item.MediaType || item.Year == 0 || c.Year != item.Year {
			continue
		}
		if !wanted[normalize(c.Title)] && !wanted[normalize(c.OriginalTitle)] {
			continue
		}
		if found != 0 && found != c.TMDBID {
			return 0
		}
		found = c.TMDBID
	}
	return found
}

func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
