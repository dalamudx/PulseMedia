package watchlist_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
)

type metadata struct {
	search       func(context.Context, string, string, int) (ports.MovieIdentitySearch, error)
	movie        *ports.MovieMetadata
	tv           *ports.TVDetails
	err          error
	queries      []string
	details      int
	cancelDetail context.CancelFunc
}

func (m *metadata) SearchMediaIdentityCandidates(ctx context.Context, q, kind string, year int) (ports.MovieIdentitySearch, error) {
	m.queries = append(m.queries, q)
	if kind != "movie" && kind != "tv" || year != 2020 {
		panic("wrong identity scope")
	}
	return m.search(ctx, q, kind, year)
}
func (m *metadata) GetMovieDetails(_ context.Context, id int64, language string) (*ports.MovieMetadata, error) {
	m.details++
	if id != 7 || language != "zh-CN" {
		panic("wrong movie detail")
	}
	if m.cancelDetail != nil {
		m.cancelDetail()
	}
	return m.movie, m.err
}
func (m *metadata) GetTVDetails(_ context.Context, id int64, language string) (*ports.TVDetails, error) {
	m.details++
	if id != 7 || language != "zh-CN" {
		panic("wrong tv detail")
	}
	if m.cancelDetail != nil {
		m.cancelDetail()
	}
	return m.tv, m.err
}
func candidate(id int64, kind, title string, year int) *ports.MovieMetadata {
	return &ports.MovieMetadata{TMDBID: id, MediaType: kind, Title: title, Year: year}
}
func fixed(candidates ...*ports.MovieMetadata) func(context.Context, string, string, int) (ports.MovieIdentitySearch, error) {
	return func(context.Context, string, string, int) (ports.MovieIdentitySearch, error) {
		return ports.MovieIdentitySearch{Candidates: candidates, Complete: true}, nil
	}
}
func item(kind string) ports.DoubanWishItem {
	return ports.DoubanWishItem{DoubanID: "123", Titles: []string{"测试作品", "Test Work"}, Year: 2020, MediaType: kind}
}
func meta() *metadata {
	return &metadata{search: fixed(candidate(7, "movie", "测试作品", 2020)), movie: candidate(7, "movie", "测试作品", 2020), tv: &ports.TVDetails{TMDBID: 7, Title: "测试作品", Year: 2020}}
}

func TestResolveWishIdentityAcrossEveryAlias(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			m := meta()
			m.search = fixed(candidate(7, kind, "Test Work", 2020), candidate(9, kind, "Other", 2020))
			got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), item(kind))
			if err != nil || got.State != watchlist.IdentityMatched || got.TMDBID != 7 || got.MediaType != kind || len(m.queries) != 2 || m.details != 1 {
				t.Fatalf("identity=%+v error=%v queries=%v details=%d", got, err, m.queries, m.details)
			}
		})
	}
	t.Run("different_aliases_are_ambiguous", func(t *testing.T) {
		m := meta()
		m.search = func(_ context.Context, q, _ string, _ int) (ports.MovieIdentitySearch, error) {
			id := int64(7)
			if q == "Test Work" {
				id = 8
			}
			return ports.MovieIdentitySearch{Complete: true, Candidates: []*ports.MovieMetadata{candidate(id, "movie", q, 2020)}}, nil
		}
		got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), item("movie"))
		if err != nil || got.State != watchlist.IdentityAmbiguous || got.TMDBID != 0 || len(m.queries) != 2 || m.details != 0 {
			t.Fatalf("%+v %v queries=%v details=%d", got, err, m.queries, m.details)
		}
	})
	t.Run("caller_snapshot", func(t *testing.T) {
		in := item("movie")
		m := meta()
		m.search = func(context.Context, string, string, int) (ports.MovieIdentitySearch, error) {
			in.Titles[1] = "mutated"
			return ports.MovieIdentitySearch{Complete: true, Candidates: []*ports.MovieMetadata{candidate(7, "movie", "测试作品", 2020)}}, nil
		}
		got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), in)
		if err != nil || got.TMDBID != 7 || len(m.queries) != 2 || m.queries[1] != "Test Work" {
			t.Fatalf("%+v %v queries=%v", got, err, m.queries)
		}
	})
}

func TestResolveWishIdentityWithholdsInsufficientProof(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*metadata)
		state   watchlist.IdentityState
		wantErr error
		details int
	}{
		{"empty", func(m *metadata) { m.search = fixed() }, watchlist.IdentityUnresolved, nil, 0},
		{"incomplete", func(m *metadata) {
			m.search = func(context.Context, string, string, int) (ports.MovieIdentitySearch, error) {
				return ports.MovieIdentitySearch{Complete: false, Candidates: []*ports.MovieMetadata{candidate(7, "movie", "测试作品", 2020)}}, nil
			}
		}, watchlist.IdentityIncomplete, nil, 0},
		{"unknown_candidate_year", func(m *metadata) {
			m.search = fixed(candidate(7, "movie", "测试作品", 2020), candidate(8, "movie", "Test Work", 0))
		}, watchlist.IdentityIncomplete, nil, 0},
		{"known_other_year", func(m *metadata) { m.search = fixed(candidate(7, "movie", "测试作品", 2019)) }, watchlist.IdentityUnresolved, nil, 0},
		{"cross_namespace", func(m *metadata) { m.search = fixed(candidate(7, "tv", "测试作品", 2020)) }, "", ports.ErrBadResponse, 0},
		{"nil_candidate", func(m *metadata) { m.search = fixed(nil) }, "", ports.ErrBadResponse, 0},
		{"bad_candidate_id", func(m *metadata) { m.search = fixed(candidate(0, "movie", "测试作品", 2020)) }, "", ports.ErrBadResponse, 0},
		{"blank_candidate_title", func(m *metadata) { m.search = fixed(candidate(7, "movie", "", 2020)) }, "", ports.ErrBadResponse, 0},
		{"detail_year", func(m *metadata) { m.movie.Year = 2021 }, watchlist.IdentityUnresolved, nil, 1},
		{"detail_namespace", func(m *metadata) { m.movie.MediaType = "tv" }, watchlist.IdentityUnresolved, nil, 1},
		{"detail_id", func(m *metadata) { m.movie.TMDBID = 8 }, watchlist.IdentityUnresolved, nil, 1},
		{"detail_title", func(m *metadata) { m.movie.Title = "Other" }, watchlist.IdentityUnresolved, nil, 1},
		{"detail_nil", func(m *metadata) { m.movie = nil }, "", ports.ErrBadResponse, 1},
		{"detail_removed", func(m *metadata) { m.err = ports.ErrNotFound }, watchlist.IdentityUnresolved, nil, 1},
		{"detail_failure", func(m *metadata) { m.err = errors.New("https://secret.invalid/?apikey=hidden") }, "", ports.ErrUnavailable, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := meta()
			c.setup(m)
			got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), item("movie"))
			if !errors.Is(err, c.wantErr) || got.State != c.state || got.TMDBID != 0 || m.details != c.details || err != nil && strings.Contains(err.Error(), "hidden") {
				t.Fatalf("%+v err=%v details=%d", got, err, m.details)
			}
		})
	}
}

func TestResolveWishIdentityKeepsTitleDistinctions(t *testing.T) {
	cases := []struct {
		source, found string
		matched       bool
	}{
		{"Cafe\u0301", "CAFÉ", true},
		{"  A\tB ", "a b", true},
		{"A-B", "AB", false},
		{"A+B", "AB", false},
		{"A/B", "AB", false},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			m := meta()
			m.search = fixed(candidate(7, "movie", c.found, 2020))
			m.movie.Title = c.found
			in := item("movie")
			in.Titles = []string{c.source}
			got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), in)
			if err != nil || (got.State == watchlist.IdentityMatched) != c.matched || (got.TMDBID == 7) != c.matched {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestResolveWishIdentityInputAndFailureFences(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*ports.DoubanWishItem)
	}{
		{"uid", func(i *ports.DoubanWishItem) { i.DoubanID = "../123" }},
		{"zero_uid", func(i *ports.DoubanWishItem) { i.DoubanID = "0" }},
		{"padded_uid", func(i *ports.DoubanWishItem) { i.DoubanID = "0123" }},
		{"type", func(i *ports.DoubanWishItem) { i.MediaType = "all" }},
		{"year", func(i *ports.DoubanWishItem) { i.Year = -1 }},
		{"no_titles", func(i *ports.DoubanWishItem) { i.Titles = nil }},
		{"blank_title", func(i *ports.DoubanWishItem) { i.Titles = []string{" "} }},
		{"nul", func(i *ports.DoubanWishItem) { i.Titles = []string{"A\x00B"} }},
		{"utf8", func(i *ports.DoubanWishItem) { i.Titles = []string{string([]byte{255})} }},
		{"long_title", func(i *ports.DoubanWishItem) { i.Titles = []string{strings.Repeat("A", 513)} }},
		{"many_titles", func(i *ports.DoubanWishItem) { i.Titles = make([]string, 9) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := item("movie")
			c.edit(&in)
			m := meta()
			got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), in)
			if !errors.Is(err, ports.ErrRejected) || got.TMDBID != 0 || len(m.queries) != 0 || m.details != 0 {
				t.Fatalf("%+v %v queries=%d", got, err, len(m.queries))
			}
		})
	}
	t.Run("unknown_year_no_IO", func(t *testing.T) {
		in := item("movie")
		in.Year = 0
		m := meta()
		got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), in)
		if err != nil || got.State != watchlist.IdentityUnresolved || len(m.queries) != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("later_alias_failure", func(t *testing.T) {
		m := meta()
		m.search = func(_ context.Context, q, _ string, _ int) (ports.MovieIdentitySearch, error) {
			if q == "Test Work" {
				return ports.MovieIdentitySearch{}, errors.New("https://hidden.invalid/")
			}
			return ports.MovieIdentitySearch{Complete: true, Candidates: []*ports.MovieMetadata{candidate(7, "movie", "测试作品", 2020)}}, nil
		}
		got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), item("movie"))
		if !errors.Is(err, ports.ErrUnavailable) || got.TMDBID != 0 || m.details != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	for _, stage := range []string{"before", "search", "detail"} {
		t.Run("cancel_"+stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := meta()
			switch stage {
			case "before":
				cancel()
			case "search":
				m.search = func(context.Context, string, string, int) (ports.MovieIdentitySearch, error) {
					cancel()
					return ports.MovieIdentitySearch{Complete: true, Candidates: []*ports.MovieMetadata{candidate(7, "movie", "测试作品", 2020)}}, nil
				}
			default:
				m.cancelDetail = cancel
			}
			got, err := watchlist.NewIdentityResolver(m).Resolve(ctx, item("movie"))
			if !errors.Is(err, context.Canceled) || got.TMDBID != 0 {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	t.Run("redacted_rate_limit", func(t *testing.T) {
		m := meta()
		m.err = ports.NewRateLimitError(10*time.Hour, "hidden cookie")
		got, err := watchlist.NewIdentityResolver(m).Resolve(context.Background(), item("movie"))
		rate, ok := ports.IsRateLimited(err)
		if !ok || rate.RetryAfter != time.Hour || strings.Contains(err.Error(), "hidden") || got.TMDBID != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
}
