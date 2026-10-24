// Package calendar serves Bangumi broadcast-weekday metadata without subscription authority.
package calendar

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type Result struct {
	Days       []ports.BangumiDay
	ObservedAt time.Time
}
type Service struct {
	provider ports.BangumiCalendarProvider
	clock    ports.Clock
	gate     chan struct{}
	mu       sync.Mutex
	cached   *Result
	expires  time.Time
}

func NewService(provider ports.BangumiCalendarProvider, clock ports.Clock) *Service {
	return &Service{provider: provider, clock: clock, gate: make(chan struct{}, 1)}
}
func (s *Service) List(ctx context.Context, weekday int, query string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if weekday < 0 || weekday > 7 || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 120 {
		return Result{}, ports.ErrRejected
	}
	for _, r := range query {
		if unicode.IsControl(r) {
			return Result{}, ports.ErrRejected
		}
	}
	if s == nil || s.provider == nil || s.clock == nil {
		return Result{}, ports.ErrUnavailable
	}
	result, ok := s.readCache()
	if !ok {
		select {
		case s.gate <- struct{}{}:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		defer func() { <-s.gate }()
		result, ok = s.readCache()
		if !ok {
			days, err := s.provider.GetCalendar(ctx)
			if err != nil {
				return Result{}, err
			}
			if days == nil {
				return Result{}, ports.ErrBadResponse
			}
			if err = ctx.Err(); err != nil {
				return Result{}, err
			}
			result = Result{Days: days, ObservedAt: s.clock.Now().UTC()}
			s.mu.Lock()
			copy := clone(result)
			s.cached = &copy
			s.expires = result.ObservedAt.Add(5 * time.Minute)
			s.mu.Unlock()
		}
	}
	query = fold(query)
	filtered := Result{Days: make([]ports.BangumiDay, 0, len(result.Days)), ObservedAt: result.ObservedAt}
	for _, day := range result.Days {
		if weekday != 0 && day.DayID != weekday {
			continue
		}
		out := ports.BangumiDay{DayID: day.DayID, DayName: day.DayName, Items: make([]ports.BangumiItem, 0, len(day.Items))}
		for _, item := range day.Items {
			if query == "" || strings.Contains(fold(item.Title), query) || strings.Contains(fold(item.OrigTitle), query) {
				out.Items = append(out.Items, item)
			}
		}
		filtered.Days = append(filtered.Days, out)
	}
	return clone(filtered), nil
}
func fold(s string) string { return cases.Fold().String(norm.NFKC.String(strings.TrimSpace(s))) }
func (s *Service) readCache() (Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached == nil || !s.clock.Now().Before(s.expires) {
		return Result{}, false
	}
	return clone(*s.cached), true
}
func clone(in Result) Result {
	out := Result{ObservedAt: in.ObservedAt, Days: make([]ports.BangumiDay, len(in.Days))}
	for i, day := range in.Days {
		out.Days[i] = day
		out.Days[i].Items = append([]ports.BangumiItem{}, day.Items...)
		for j, item := range day.Items {
			if item.Rating != nil {
				v := *item.Rating
				out.Days[i].Items[j].Rating = &v
			}
		}
	}
	return out
}
