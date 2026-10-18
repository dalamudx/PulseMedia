package subscription_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

type creationMetadataProvider struct {
	movie func(context.Context, int64, string) (*ports.MovieMetadata, error)
	tv    func(context.Context, int64, string) (*ports.TVDetails, error)
}

func (p *creationMetadataProvider) GetMovieDetails(ctx context.Context, id int64, language string) (*ports.MovieMetadata, error) {
	return p.movie(ctx, id, language)
}
func (p *creationMetadataProvider) GetTVDetails(ctx context.Context, id int64, language string) (*ports.TVDetails, error) {
	return p.tv(ctx, id, language)
}
func (*creationMetadataProvider) SearchMovies(context.Context, string, int) ([]*ports.MovieMetadata, error) {
	panic("unexpected search")
}
func (*creationMetadataProvider) SearchMedia(context.Context, string, string, int) ([]*ports.MovieMetadata, error) {
	panic("unexpected search")
}
func (*creationMetadataProvider) FindMovieByExternalID(context.Context, string, string) (*ports.MovieMetadata, error) {
	panic("unexpected external lookup")
}
func (*creationMetadataProvider) GetDiscoverFeed(context.Context, string, int) ([]*ports.MovieMetadata, error) {
	panic("unexpected feed")
}

type creationEpisodeProvider func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error)

func (p creationEpisodeProvider) GetTVSeasonMetadata(ctx context.Context, id int64, season int, language string) (*ports.TVSeasonMetadata, error) {
	return p(ctx, id, season, language)
}

func creationDetails(seasons ...ports.TVSeasonDetails) *ports.TVDetails {
	return &ports.TVDetails{TMDBID: 42, Title: "Provider TV", Year: 2024, Seasons: append([]ports.TVSeasonDetails{}, seasons...)}
}
func creationProvider(tv *ports.TVDetails) *creationMetadataProvider {
	return &creationMetadataProvider{
		movie: func(context.Context, int64, string) (*ports.MovieMetadata, error) {
			return &ports.MovieMetadata{TMDBID: 42, Title: "Provider Movie", MediaType: "movie", Year: 2024}, nil
		},
		tv: func(context.Context, int64, string) (*ports.TVDetails, error) { return tv, nil },
	}
}
func creationSeason(season int, numbers ...int) *ports.TVSeasonMetadata {
	out := &ports.TVSeasonMetadata{SeasonNumber: season, Complete: true, Episodes: make([]ports.TVEpisodeMetadata, 0, len(numbers))}
	for _, number := range numbers {
		out.Episodes = append(out.Episodes, ports.TVEpisodeMetadata{TMDBEpisodeID: int64(100000 + season*10000 + number), EpisodeNumber: number})
	}
	return out
}
func creationRequireError(t *testing.T, result *ports.PreparedSubscriptionCreationMetadata, err, want error) {
	t.Helper()
	if result != nil || !errors.Is(err, want) {
		t.Fatalf("got result=%+v error=%v; want nil result and %v", result, err, want)
	}
}

func TestPrepareSubscriptionCreationMetadataMovieAndNamespaces(t *testing.T) {
	for _, year := range []int{0, 1, 2024, 9999} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			p := creationProvider(creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 1}))
			p.movie = func(ctx context.Context, id int64, language string) (*ports.MovieMetadata, error) {
				if ctx.Err() != nil || id != 42 || language != "zh-CN" {
					t.Fatalf("wrong movie request: id=%d language=%q error=%v", id, language, ctx.Err())
				}
				return &ports.MovieMetadata{TMDBID: 42, MediaType: "movie", Title: "  ", OriginalTitle: " Native original ", Year: year}, nil
			}
			movie, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), p, nil, "movie", 42, nil, false)
			if err != nil || movie.Media.Title != "Native original" || movie.Media.Namespace != "tmdb.movie" || movie.Media.ExternalID != "42" || movie.TMDBID != 42 || movie.Media.MediaType != "movie" {
				t.Fatalf("movie=%+v error=%v", movie, err)
			}
			if (year == 0 && movie.Media.Year != nil) || (year != 0 && (movie.Media.Year == nil || *movie.Media.Year != int32(year))) {
				t.Fatalf("wrong year: %+v", movie.Media.Year)
			}
			if len(movie.Units) != 1 || movie.Units[0].UnitKey != "movie" || movie.Units[0].SeasonNumber != nil || movie.Units[0].EpisodeNumber != nil || movie.Seasons == nil || movie.Media.Seasons == nil || len(movie.Seasons) != 0 {
				t.Fatalf("invalid movie evidence: %+v", movie)
			}
			tv, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), p, creationEpisodeProvider(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) {
				return creationSeason(1, 3), nil
			}), "tv", 42, nil, false)
			if err != nil || tv.Media.Namespace != "tmdb.tv" || tv.Media.ExternalID != movie.Media.ExternalID || tv.Media.MediaType != "tv" || tv.Units[0].UnitKey != "S01E03" {
				t.Fatalf("cross-namespace confusion: tv=%+v error=%v", tv, err)
			}
		})
	}
}

func TestPrepareSubscriptionCreationMetadataSparseAndOwned(t *testing.T) {
	date := time.Date(2099, time.February, 28, 0, 0, 0, 0, time.UTC)
	absolute := 27
	response := creationSeason(2, 3, 1)
	for i := range response.Episodes {
		response.Episodes[i].AirDate = &date
		response.Episodes[i].AbsoluteNumber = &absolute
	}
	details := creationDetails(ports.TVSeasonDetails{SeasonNumber: 2, EpisodeCount: 2}, ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 0})
	details.Title, details.OriginalTitle = "", "Original TV"
	requested := []int{2, 1}
	provider := creationProvider(details)
	episodes := creationEpisodeProvider(func(ctx context.Context, id int64, season int, language string) (*ports.TVSeasonMetadata, error) {
		if id != 42 || season != 2 || language != "zh-CN" || ctx.Err() != nil {
			return nil, errors.New("incorrect season request")
		}
		return response, nil
	})
	first, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), provider, episodes, "tv", 42, requested, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), provider, episodes, "tv", 42, requested, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Media.Title != "Original TV" || !reflect.DeepEqual(requested, []int{2, 1}) || len(first.Units) != 2 || first.Units[0].UnitKey != "S02E01" || first.Units[1].UnitKey != "S02E03" || len(first.Seasons) != 1 || first.Seasons[0].Episodes[0].EpisodeNumber != 1 || first.Seasons[0].Episodes[1].EpisodeNumber != 3 {
		t.Fatalf("sparse episodes changed: %+v", first)
	}
	if !reflect.DeepEqual(first.Media.Seasons, []ports.SeasonEpisodes{{Season: 1, Count: 0}, {Season: 2, Count: 2}}) {
		t.Fatalf("wrong count truth: %+v", first.Media.Seasons)
	}
	if !first.Seasons[0].Episodes[0].AirDate.Equal(date) || *first.Seasons[0].Episodes[0].AbsoluteNumber != absolute {
		t.Fatal("lost observed future date/absolute number")
	}
	// Mutate every pointer and slice shape exposed by the result.
	*first.Media.Year = 2
	first.Media.Seasons[0].Count = 5000
	*first.Units[0].SeasonNumber = 9
	*first.Units[0].EpisodeNumber = 9
	first.Units[0].UnitKey = "changed"
	*first.Seasons[0].Episodes[0].AirDate = time.Time{}
	*first.Seasons[0].Episodes[0].AbsoluteNumber = 9
	first.Seasons[0].Episodes[0].TMDBEpisodeID = 9
	if *first.Units[1].SeasonNumber != 2 || *first.Units[1].EpisodeNumber != 3 || *first.Seasons[0].Episodes[1].AbsoluteNumber != 27 || !first.Seasons[0].Episodes[1].AirDate.Equal(date) || absolute != 27 || response.Episodes[1].TMDBEpisodeID == 9 {
		t.Fatal("result aliases a sibling or provider")
	}
	requested[0] = 999
	details.Year = 1
	details.Seasons[0].EpisodeCount = 100
	response.Episodes[0].EpisodeNumber = 999
	absolute = 888
	date = time.Time{}
	if *second.Media.Year != 2024 || second.Media.Seasons[1].Count != 2 || second.Seasons[0].Episodes[1].EpisodeNumber != 3 || *second.Seasons[0].Episodes[0].AbsoluteNumber != 27 || second.Seasons[0].Episodes[0].AirDate.Year() != 2099 || *second.Units[0].SeasonNumber != 2 || *second.Units[0].EpisodeNumber != 1 {
		t.Fatal("prepared evidence aliases input or another result")
	}
}

func TestPrepareSubscriptionCreationMetadataSeasonSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested []int
		specials  bool
		want      []ports.SeasonEpisodes
	}{
		{"ordinary", nil, false, []ports.SeasonEpisodes{{Season: 1, Count: 1}, {Season: 2, Count: 0}}},
		{"all and specials", nil, true, []ports.SeasonEpisodes{{Season: 0, Count: 1}, {Season: 1, Count: 1}, {Season: 2, Count: 0}}},
		{"selected", []int{2, 1}, false, []ports.SeasonEpisodes{{Season: 1, Count: 1}, {Season: 2, Count: 0}}},
		{"selected plus specials", []int{2}, true, []ports.SeasonEpisodes{{Season: 0, Count: 1}, {Season: 2, Count: 0}}},
		{"explicit specials only", []int{0}, true, []ports.SeasonEpisodes{{Season: 0, Count: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			details := creationDetails(ports.TVSeasonDetails{SeasonNumber: 2, EpisodeCount: 0}, ports.TVSeasonDetails{SeasonNumber: 0, EpisodeCount: 1}, ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 1})
			details.SeasonsCount, details.EpisodesCount = 2, 1 // Specials excluded from summaries.
			var mu sync.Mutex
			calls := map[int]int{}
			episodes := creationEpisodeProvider(func(_ context.Context, id int64, season int, language string) (*ports.TVSeasonMetadata, error) {
				mu.Lock()
				calls[season]++
				mu.Unlock()
				if id != 42 || language != "zh-CN" {
					return nil, errors.New("incorrect request")
				}
				return creationSeason(season, 3), nil
			})
			out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(details), episodes, "tv", 42, tc.requested, tc.specials)
			if err != nil || !reflect.DeepEqual(out.Media.Seasons, tc.want) {
				t.Fatalf("out=%+v error=%v want=%+v", out, err, tc.want)
			}
			observations := 0
			for _, selected := range tc.want {
				if selected.Count > 0 {
					observations++
					if calls[selected.Season] != 1 {
						t.Fatalf("missing season call: %+v", calls)
					}
				}
			}
			if len(calls) != observations || len(out.Seasons) != observations || len(out.Units) != observations {
				t.Fatalf("unexpected network/evidence: calls=%+v out=%+v", calls, out)
			}
		})
	}
}

func TestPrepareSubscriptionCreationMetadataEmptyAndUnknown(t *testing.T) {
	for _, seasons := range [][]ports.TVSeasonDetails{{}, {{SeasonNumber: 1, EpisodeCount: 0}}, {{SeasonNumber: 0, EpisodeCount: 1}}} {
		details := creationDetails(seasons...)
		details.Year = 0
		out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(details), nil, "tv", 42, nil, false)
		if err != nil || out.Units == nil || out.Seasons == nil || out.Media.Seasons == nil || len(out.Units) != 0 || len(out.Seasons) != 0 || out.Media.Year != nil {
			t.Fatalf("announced empty/unknown must stay pending: out=%+v error=%v", out, err)
		}
	}
	response := creationSeason(1, 1, 3)
	response.Episodes[1].AirDate = new(time.Date(2024, time.February, 29, 0, 0, 0, 0, time.UTC))
	response.Episodes[1].AbsoluteNumber = new(math.MaxInt32)
	out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 2})), creationEpisodeProvider(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) { return response, nil }), "tv", 42, nil, false)
	if err != nil || out.Seasons[0].Episodes[0].AirDate != nil || out.Seasons[0].Episodes[0].AbsoluteNumber != nil || *out.Seasons[0].Episodes[1].AbsoluteNumber != math.MaxInt32 {
		t.Fatalf("unknowns fabricated or valid bounds lost: out=%+v error=%v", out, err)
	}
}

func TestPrepareSubscriptionCreationMetadataInvalidRequest(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		id         int64
		seasons    []int
		specials   bool
	}{
		{"missing type", "", 42, nil, false}, {"multi", "multi", 42, nil, false}, {"case", "TV", 42, nil, false},
		{"zero ID", "movie", 0, nil, false}, {"negative ID", "tv", -1, nil, false},
		{"movie seasons", "movie", 42, []int{1}, false}, {"movie specials", "movie", 42, nil, true},
		{"negative season", "tv", 42, []int{-1}, false}, {"over season", "tv", 42, []int{1000}, false},
		{"duplicate", "tv", 42, []int{1, 1}, false}, {"specials not allowed", "tv", 42, []int{0}, false},
		{"too many requested", "tv", 42, make([]int, 65), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), nil, nil, tc.kind, tc.id, tc.seasons, tc.specials)
			creationRequireError(t, out, err, ports.ErrInvalidSubscriptionBindings)
		})
	}
	out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), nil, nil, "movie", 42, nil, false)
	creationRequireError(t, out, err, ports.ErrUnavailable)
	out, err = subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(creationDetails()), nil, "tv", 42, []int{1}, false)
	creationRequireError(t, out, err, ports.ErrInvalidSubscriptionBindings)
	out, err = subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 1})), nil, "tv", 42, nil, false)
	creationRequireError(t, out, err, ports.ErrUnavailable)
}

func TestPrepareSubscriptionCreationMetadataBadMovie(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ports.MovieMetadata)
	}{
		{"ID", func(d *ports.MovieMetadata) { d.TMDBID = 43 }},
		{"TV masquerade", func(d *ports.MovieMetadata) { d.MediaType = "tv" }},
		{"missing native type", func(d *ports.MovieMetadata) { d.MediaType = "" }},
		{"title", func(d *ports.MovieMetadata) { d.Title = " \t " }},
		{"negative year", func(d *ports.MovieMetadata) { d.Year = -1 }},
		{"large year", func(d *ports.MovieMetadata) { d.Year = 10000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &ports.MovieMetadata{TMDBID: 42, MediaType: "movie", Title: "Native"}
			tc.mutate(d)
			p := &creationMetadataProvider{movie: func(context.Context, int64, string) (*ports.MovieMetadata, error) { return d, nil }}
			out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), p, nil, "movie", 42, nil, false)
			creationRequireError(t, out, err, ports.ErrBadResponse)
		})
	}
}

func TestPrepareSubscriptionCreationMetadataBadTV(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ports.TVDetails)
	}{
		{"ID", func(d *ports.TVDetails) { d.TMDBID++ }},
		{"title", func(d *ports.TVDetails) { d.Title = " " }},
		{"negative year", func(d *ports.TVDetails) { d.Year = -1 }},
		{"large year", func(d *ports.TVDetails) { d.Year = 10000 }},
		{"nil seasons", func(d *ports.TVDetails) { d.Seasons = nil }},
		{"negative season", func(d *ports.TVDetails) { d.Seasons[0].SeasonNumber = -1 }},
		{"large season", func(d *ports.TVDetails) { d.Seasons[0].SeasonNumber = 1000 }},
		{"negative count", func(d *ports.TVDetails) { d.Seasons[0].EpisodeCount = -1 }},
		{"large count", func(d *ports.TVDetails) { d.Seasons[0].EpisodeCount = 5001 }},
		{"duplicate season", func(d *ports.TVDetails) { d.Seasons = append(d.Seasons, d.Seasons[0]) }},
		{"too many seasons", func(d *ports.TVDetails) { d.Seasons = make([]ports.TVSeasonDetails, 65) }},
		{"negative season summary", func(d *ports.TVDetails) { d.SeasonsCount = -1 }},
		{"negative episode summary", func(d *ports.TVDetails) { d.EpisodesCount = -1 }},
		{"conflicting season summary", func(d *ports.TVDetails) { d.SeasonsCount = 2 }},
		{"conflicting episode summary", func(d *ports.TVDetails) { d.EpisodesCount = 3 }},
		{"total over bound", func(d *ports.TVDetails) {
			d.Seasons = []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 5000}, {SeasonNumber: 2, EpisodeCount: 5000}, {SeasonNumber: 3, EpisodeCount: 5000}, {SeasonNumber: 4, EpisodeCount: 5000}, {SeasonNumber: 5, EpisodeCount: 1}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 2})
			tc.mutate(d)
			out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(d), nil, "tv", 42, nil, false)
			creationRequireError(t, out, err, ports.ErrBadResponse)
		})
	}
}

func TestPrepareSubscriptionCreationMetadataBadEpisodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ports.TVSeasonMetadata)
	}{
		{"incomplete", func(s *ports.TVSeasonMetadata) { s.Complete = false }},
		{"wrong season", func(s *ports.TVSeasonMetadata) { s.SeasonNumber = 0 }},
		{"empty", func(s *ports.TVSeasonMetadata) { s.Episodes = nil }},
		{"short count", func(s *ports.TVSeasonMetadata) { s.Episodes = s.Episodes[:1] }},
		{"long count", func(s *ports.TVSeasonMetadata) {
			s.Episodes = append(s.Episodes, ports.TVEpisodeMetadata{TMDBEpisodeID: 99, EpisodeNumber: 4})
		}},
		{"duplicate number", func(s *ports.TVSeasonMetadata) { s.Episodes[1].EpisodeNumber = 1 }},
		{"duplicate provider ID", func(s *ports.TVSeasonMetadata) { s.Episodes[1].TMDBEpisodeID = s.Episodes[0].TMDBEpisodeID }},
		{"legacy zero ID", func(s *ports.TVSeasonMetadata) { s.Episodes[1].TMDBEpisodeID = 0 }},
		{"negative ID", func(s *ports.TVSeasonMetadata) { s.Episodes[1].TMDBEpisodeID = -1 }},
		{"zero ordinal", func(s *ports.TVSeasonMetadata) { s.Episodes[1].EpisodeNumber = 0 }},
		{"negative ordinal", func(s *ports.TVSeasonMetadata) { s.Episodes[1].EpisodeNumber = -1 }},
		{"large ordinal", func(s *ports.TVSeasonMetadata) { s.Episodes[1].EpisodeNumber = 10000 }},
		{"zero absolute", func(s *ports.TVSeasonMetadata) { s.Episodes[1].AbsoluteNumber = new(0) }},
		{"negative absolute", func(s *ports.TVSeasonMetadata) { s.Episodes[1].AbsoluteNumber = new(-1) }},
		{"large absolute", func(s *ports.TVSeasonMetadata) { s.Episodes[1].AbsoluteNumber = new(int(int64(math.MaxInt32) + 1)) }},
		{"year zero", func(s *ports.TVSeasonMetadata) { s.Episodes[1].AirDate = new(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)) }},
		{"year too large", func(s *ports.TVSeasonMetadata) {
			s.Episodes[1].AirDate = new(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := creationSeason(1, 1, 3)
			tc.mutate(response)
			out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 2})), creationEpisodeProvider(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) { return response, nil }), "tv", 42, nil, false)
			creationRequireError(t, out, err, ports.ErrBadResponse)
		})
	}
	out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 1}, ports.TVSeasonDetails{SeasonNumber: 2, EpisodeCount: 1})), creationEpisodeProvider(func(_ context.Context, _ int64, season int, _ string) (*ports.TVSeasonMetadata, error) {
		s := creationSeason(season, 1)
		s.Episodes[0].TMDBEpisodeID = 123
		return s, nil
	}), "tv", 42, nil, false)
	creationRequireError(t, out, err, ports.ErrBadResponse)
}

func TestPrepareSubscriptionCreationMetadataLimits(t *testing.T) {
	seasons := make([]ports.TVSeasonDetails, 64)
	for i := range seasons {
		seasons[i].SeasonNumber = i + 1
	}
	seasons[63].SeasonNumber = 999
	out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(creationDetails(seasons...)), nil, "tv", 42, nil, false)
	if err != nil || len(out.Media.Seasons) != 64 || out.Media.Seasons[63].Season != 999 {
		t.Fatalf("64 seasons/max season failed: out=%+v error=%v", out, err)
	}
	seasons = []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 5000}, {SeasonNumber: 2, EpisodeCount: 5000}, {SeasonNumber: 3, EpisodeCount: 5000}, {SeasonNumber: 4, EpisodeCount: 5000}}
	out, err = subscription.PrepareSubscriptionCreationMetadata(t.Context(), creationProvider(creationDetails(seasons...)), creationEpisodeProvider(func(_ context.Context, _ int64, season int, _ string) (*ports.TVSeasonMetadata, error) {
		numbers := make([]int, 5000)
		for i := range numbers {
			numbers[i] = i + 1
		}
		numbers[4999] = 9999 // Count 5000, sparse max ordinal 9999.
		return creationSeason(season, numbers...), nil
	}), "tv", 42, nil, false)
	if err != nil || len(out.Units) != 20000 || len(out.Seasons) != 4 || out.Units[19999].UnitKey != "S04E9999" {
		t.Fatalf("exact episode limits failed: error=%v", err)
	}
}

func TestPrepareSubscriptionCreationMetadataErrorsAreSafe(t *testing.T) {
	for _, stage := range []string{"movie", "tv", "episodes"} {
		for _, tc := range []struct {
			name         string
			source, want error
		}{
			{"raw", errors.New("https://user:secret@example.invalid/?api_key=TOPSECRET"), ports.ErrUnavailable},
			{"unavailable", fmt.Errorf("TOPSECRET: %w", ports.ErrUnavailable), ports.ErrUnavailable},
			{"bad response", fmt.Errorf("TOPSECRET: %w", ports.ErrBadResponse), ports.ErrBadResponse},
			{"timeout", fmt.Errorf("TOPSECRET: %w", ports.ErrTimeout), ports.ErrTimeout},
			{"deadline", fmt.Errorf("TOPSECRET: %w", context.DeadlineExceeded), context.DeadlineExceeded},
			{"cancelled", fmt.Errorf("TOPSECRET: %w", context.Canceled), context.Canceled},
		} {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				p := creationProvider(creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 1}))
				episodes := creationEpisodeProvider(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) {
					return creationSeason(1, 1), tc.source
				})
				kind := "tv"
				switch stage {
				case "movie":
					kind = "movie"
					p.movie = func(context.Context, int64, string) (*ports.MovieMetadata, error) {
						return &ports.MovieMetadata{TMDBID: 42}, tc.source
					}
				case "tv":
					p.tv = func(context.Context, int64, string) (*ports.TVDetails, error) { return creationDetails(), tc.source }
				}
				out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), p, episodes, kind, 42, nil, false)
				creationRequireError(t, out, err, tc.want)
				if err != tc.want || strings.Contains(err.Error(), "TOPSECRET") || strings.Contains(err.Error(), "secret") {
					t.Fatalf("raw provider error escaped: %v", err)
				}
			})
		}
	}
	for _, stage := range []string{"movie", "tv", "episodes"} {
		t.Run(stage+"/nil response", func(t *testing.T) {
			p := creationProvider(creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 1}))
			kind := "tv"
			switch stage {
			case "movie":
				kind = "movie"
				p.movie = func(context.Context, int64, string) (*ports.MovieMetadata, error) { return nil, nil }
			case "tv":
				p.tv = func(context.Context, int64, string) (*ports.TVDetails, error) { return nil, nil }
			}
			out, err := subscription.PrepareSubscriptionCreationMetadata(t.Context(), p, creationEpisodeProvider(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) { return nil, nil }), kind, 42, nil, false)
			creationRequireError(t, out, err, ports.ErrBadResponse)
		})
	}
}

type creationMetadataResult struct {
	out *ports.PreparedSubscriptionCreationMetadata
	err error
}

// Every async test joins its owned invocation even if an assertion fails.
func creationStart(t *testing.T, run func() (*ports.PreparedSubscriptionCreationMetadata, error), release func()) <-chan creationMetadataResult {
	t.Helper()
	result := make(chan creationMetadataResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		out, err := run()
		result <- creationMetadataResult{out, err}
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("owned preparation call failed to join")
		}
	})
	return result
}
func creationReceive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("controlled provider did not reach expected boundary")
		var zero T
		return zero
	}
}

func TestPrepareSubscriptionCreationMetadataFourPhysicalCalls(t *testing.T) {
	seasons := make([]ports.TVSeasonDetails, 12)
	for i := range seasons {
		seasons[i] = ports.TVSeasonDetails{SeasonNumber: i + 1, EpisodeCount: 1}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan int, 12)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var active, maximum, calls atomic.Int32
	episodes := creationEpisodeProvider(func(_ context.Context, _ int64, season int, _ string) (*ports.TVSeasonMetadata, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		calls.Add(1)
		started <- season
		<-release
		return creationSeason(season, 1), nil
	})
	result := creationStart(t, func() (*ports.PreparedSubscriptionCreationMetadata, error) {
		return subscription.PrepareSubscriptionCreationMetadata(ctx, creationProvider(creationDetails(seasons...)), episodes, "tv", 42, nil, false)
	}, func() { cancel(); unblock() })
	for range 4 {
		creationReceive(t, started)
	}
	if maximum.Load() != 4 || active.Load() != 4 || calls.Load() != 4 {
		t.Fatalf("physical budget violated: max=%d active=%d calls=%d", maximum.Load(), active.Load(), calls.Load())
	}
	unblock()
	got := creationReceive(t, result)
	if got.err != nil || len(got.out.Units) != 12 || maximum.Load() != 4 || active.Load() != 0 || calls.Load() != 12 {
		t.Fatalf("bad joined outcome: error=%v max=%d active=%d calls=%d", got.err, maximum.Load(), active.Load(), calls.Load())
	}
	for i, unit := range got.out.Units {
		if *unit.SeasonNumber != int32(i+1) {
			t.Fatal("concurrent results not sorted")
		}
	}
}

func TestPrepareSubscriptionCreationMetadataCancellationJoinsIgnoringProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, cancelled := make(chan struct{}, 2), make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var active atomic.Int32
	episodes := creationEpisodeProvider(func(child context.Context, _ int64, season int, _ string) (*ports.TVSeasonMetadata, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-child.Done()
		cancelled <- struct{}{}
		<-release // Intentionally ignore cancellation and eventually return success.
		return creationSeason(season, 1), nil
	})
	result := creationStart(t, func() (*ports.PreparedSubscriptionCreationMetadata, error) {
		return subscription.PrepareSubscriptionCreationMetadata(ctx, creationProvider(creationDetails(ports.TVSeasonDetails{SeasonNumber: 1, EpisodeCount: 1}, ports.TVSeasonDetails{SeasonNumber: 2, EpisodeCount: 1})), episodes, "tv", 42, nil, false)
	}, func() { cancel(); unblock() })
	for range 2 {
		creationReceive(t, started)
	}
	cancel()
	for range 2 {
		creationReceive(t, cancelled)
	}
	select {
	case <-result:
		t.Fatal("returned before physical calls completed")
	default:
	}
	if active.Load() != 2 {
		t.Fatal("lost physical calls after logical cancellation")
	}
	unblock()
	got := creationReceive(t, result)
	creationRequireError(t, got.out, got.err, context.Canceled)
	if active.Load() != 0 {
		t.Fatal("unjoined calls")
	}
}

func TestPrepareSubscriptionCreationMetadataFirstErrorCancelsAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan int, 4)
	fail, release := make(chan struct{}), make(chan struct{})
	cancelled := make(chan struct{}, 3)
	var failOnce, releaseOnce sync.Once
	unblock := func() { failOnce.Do(func() { close(fail) }); releaseOnce.Do(func() { close(release) }) }
	var calls, active atomic.Int32
	seasons := make([]ports.TVSeasonDetails, 10)
	for i := range seasons {
		seasons[i] = ports.TVSeasonDetails{SeasonNumber: i + 1, EpisodeCount: 1}
	}
	episodes := creationEpisodeProvider(func(child context.Context, _ int64, season int, _ string) (*ports.TVSeasonMetadata, error) {
		calls.Add(1)
		active.Add(1)
		defer active.Add(-1)
		started <- season
		if season == 1 {
			<-fail
			response := creationSeason(season, 1)
			response.Complete = false
			return response, nil
		}
		<-child.Done()
		cancelled <- struct{}{}
		<-release
		return creationSeason(season, 1), nil
	})
	result := creationStart(t, func() (*ports.PreparedSubscriptionCreationMetadata, error) {
		return subscription.PrepareSubscriptionCreationMetadata(ctx, creationProvider(creationDetails(seasons...)), episodes, "tv", 42, nil, false)
	}, func() { cancel(); unblock() })
	for range 4 {
		creationReceive(t, started)
	}
	failOnce.Do(func() { close(fail) })
	for range 3 {
		creationReceive(t, cancelled)
	}
	select {
	case <-result:
		t.Fatal("first error returned before joining ignoring clients")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	got := creationReceive(t, result)
	creationRequireError(t, got.out, got.err, ports.ErrBadResponse)
	if calls.Load() != 4 || active.Load() != 0 {
		t.Fatalf("queued calls started after failure or unjoined calls: calls=%d active=%d", calls.Load(), active.Load())
	}
}

func TestPrepareSubscriptionCreationMetadataContextBoundaries(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			out, err := subscription.PrepareSubscriptionCreationMetadata(ctx, nil, nil, kind, 42, nil, false)
			creationRequireError(t, out, err, context.Canceled)
			deadline, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			defer stop()
			out, err = subscription.PrepareSubscriptionCreationMetadata(deadline, nil, nil, kind, 42, nil, false)
			creationRequireError(t, out, err, context.DeadlineExceeded)
			ctx, cancel = context.WithCancel(t.Context())
			defer cancel()
			p := creationProvider(creationDetails())
			p.movie = func(child context.Context, _ int64, _ string) (*ports.MovieMetadata, error) {
				if child == ctx {
					t.Error("provider did not receive child context")
				}
				cancel()
				return &ports.MovieMetadata{TMDBID: 42, MediaType: "movie", Title: "Late success"}, nil
			}
			p.tv = func(child context.Context, _ int64, _ string) (*ports.TVDetails, error) {
				if child == ctx {
					t.Error("provider did not receive child context")
				}
				cancel()
				return creationDetails(), nil
			}
			out, err = subscription.PrepareSubscriptionCreationMetadata(ctx, p, nil, kind, 42, nil, false)
			creationRequireError(t, out, err, context.Canceled)
		})
	}
}
