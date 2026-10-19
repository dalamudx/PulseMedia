package subscription

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	creationMetadataMaxSeasons        = 64
	creationMetadataMaxSeasonEpisodes = 5000
	creationMetadataMaxEpisodes       = 20000
	creationMetadataConcurrency       = 4
)

// PrepareSubscriptionCreationMetadata observes native TMDB metadata outside a
// transaction. The caller must authorize and read its receipt before this I/O,
// then revalidate publication authority afterward. Counts never mint episodes.
func PrepareSubscriptionCreationMetadata(ctx context.Context, provider ports.MetadataProvider, episodes ports.TVEpisodeProvider, mediaType string, tmdbID int64, seasons []int, includeSpecials bool) (*ports.PreparedSubscriptionCreationMetadata, error) {
	if tmdbID <= 0 || (mediaType != "movie" && mediaType != "tv") || len(seasons) > creationMetadataMaxSeasons {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	if mediaType == "movie" && (len(seasons) != 0 || includeSpecials) {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	requested := append([]int(nil), seasons...)
	sort.Ints(requested)
	for i, season := range requested {
		if season < 0 || season > 999 || (season == 0 && !includeSpecials) || (i > 0 && season == requested[i-1]) {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, ports.ErrUnavailable
	}
	network, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &ports.PreparedSubscriptionCreationMetadata{
		TMDBID: tmdbID,
		Media: ports.SubscriptionMediaInput{
			Namespace: "tmdb." + mediaType, ExternalID: strconv.FormatInt(tmdbID, 10), MediaType: mediaType,
			Seasons: make([]ports.SeasonEpisodes, 0),
		},
		Units: make([]ports.SubscriptionMediaUnitSpec, 0), Seasons: make([]ports.TVSeasonMetadata, 0),
	}
	if mediaType == "movie" {
		details, err := provider.GetMovieDetails(network, tmdbID, "zh-CN")
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, creationMetadataProviderError(err)
		}
		if details == nil || details.TMDBID != tmdbID || details.MediaType != "movie" {
			return nil, ports.ErrBadResponse
		}
		out.Media.Title, out.Media.Year, err = creationMetadataTitleYear(details.Title, details.OriginalTitle, details.Year)
		if err != nil {
			return nil, err
		}
		out.PosterPath = media.TMDBPosterPath(details.PosterURL)
		out.Units = append(out.Units, ports.SubscriptionMediaUnitSpec{UnitKey: media.FormatMovieUnitKey()})
	} else {
		details, err := provider.GetTVDetails(network, tmdbID, "zh-CN")
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, creationMetadataProviderError(err)
		}
		if details == nil || details.TMDBID != tmdbID || details.Seasons == nil {
			return nil, ports.ErrBadResponse
		}
		out.Media.Title, out.Media.Year, err = creationMetadataTitleYear(details.Title, details.OriginalTitle, details.Year)
		if err != nil {
			return nil, err
		}
		out.PosterPath = media.TMDBPosterPath(details.PosterURL)
		out.Media.Seasons, err = creationMetadataSelectSeasons(details, requested, includeSpecials)
		if err != nil {
			return nil, err
		}
		out.Seasons, err = creationMetadataObserveSeasons(network, episodes, tmdbID, out.Media.Seasons)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		seenIDs := make(map[int64]bool)
		for _, season := range out.Seasons {
			for _, episode := range season.Episodes {
				if seenIDs[episode.TMDBEpisodeID] {
					return nil, ports.ErrBadResponse
				}
				seenIDs[episode.TMDBEpisodeID] = true
				sn, en := int32(season.SeasonNumber), int32(episode.EpisodeNumber)
				out.Units = append(out.Units, ports.SubscriptionMediaUnitSpec{
					UnitKey: media.FormatEpisodeUnitKey(season.SeasonNumber, episode.EpisodeNumber), SeasonNumber: &sn, EpisodeNumber: &en,
				})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func creationMetadataTitleYear(title, original string, year int) (string, *int32, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = strings.TrimSpace(original)
	}
	if title == "" || year < 0 || year > 9999 {
		return "", nil, ports.ErrBadResponse
	}
	if year == 0 {
		return title, nil, nil
	}
	return title, new(int32(year)), nil
}

func creationMetadataSelectSeasons(details *ports.TVDetails, requested []int, includeSpecials bool) ([]ports.SeasonEpisodes, error) {
	if len(details.Seasons) > creationMetadataMaxSeasons || details.SeasonsCount < 0 || details.EpisodesCount < 0 {
		return nil, ports.ErrBadResponse
	}
	declared := make(map[int]int, len(details.Seasons))
	total, ordinarySeasons, ordinaryEpisodes := 0, 0, 0
	for _, season := range details.Seasons {
		_, duplicate := declared[season.SeasonNumber]
		if season.SeasonNumber < 0 || season.SeasonNumber > 999 || season.EpisodeCount < 0 || season.EpisodeCount > creationMetadataMaxSeasonEpisodes || duplicate {
			return nil, ports.ErrBadResponse
		}
		declared[season.SeasonNumber] = season.EpisodeCount
		total += season.EpisodeCount
		if total > creationMetadataMaxEpisodes {
			return nil, ports.ErrBadResponse
		}
		if season.SeasonNumber > 0 {
			ordinarySeasons++
			ordinaryEpisodes += season.EpisodeCount
		}
	}
	// TVDetails uses int summaries: zero also represents an absent/unknown
	// summary. A positive summary must agree with all ordinary declared seasons.
	if (details.SeasonsCount > 0 && details.SeasonsCount != ordinarySeasons) || (details.EpisodesCount > 0 && details.EpisodesCount != ordinaryEpisodes) {
		return nil, ports.ErrBadResponse
	}
	selected := make(map[int]bool, len(declared))
	if len(requested) == 0 {
		for number := range declared {
			if number > 0 {
				selected[number] = true
			}
		}
	} else {
		for _, number := range requested {
			if _, exists := declared[number]; !exists {
				return nil, ports.ErrInvalidSubscriptionBindings
			}
			selected[number] = true
		}
	}
	if _, exists := declared[0]; includeSpecials && exists {
		selected[0] = true
	}
	out := make([]ports.SeasonEpisodes, 0, len(selected))
	for number := range selected {
		out = append(out, ports.SeasonEpisodes{Season: number, Count: declared[number]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Season < out[j].Season })
	return out, nil
}

func creationMetadataObserveSeasons(ctx context.Context, provider ports.TVEpisodeProvider, tmdbID int64, selected []ports.SeasonEpisodes) ([]ports.TVSeasonMetadata, error) {
	pending := make([]ports.SeasonEpisodes, 0, len(selected))
	for _, season := range selected {
		if season.Count > 0 {
			pending = append(pending, season)
		}
	}
	observed := make([]ports.TVSeasonMetadata, len(pending))
	if len(pending) == 0 {
		return observed, nil
	}
	if provider == nil {
		return nil, ports.ErrUnavailable
	}
	network, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int, len(pending))
	for i := range pending {
		jobs <- i
	}
	close(jobs)
	var firstError error
	var failed sync.Once
	var workers sync.WaitGroup
	for range min(creationMetadataConcurrency, len(pending)) {
		workers.Go(func() {
			for i := range jobs {
				if network.Err() != nil {
					return
				}
				season := pending[i]
				response, err := provider.GetTVSeasonMetadata(network, tmdbID, season.Season, "zh-CN")
				if err != nil {
					err = creationMetadataProviderError(err)
				} else if network.Err() != nil {
					err = network.Err()
				} else {
					observed[i], err = creationMetadataCopySeason(response, season)
				}
				if err != nil {
					failed.Do(func() { firstError = err; cancel() })
					return
				}
			}
		})
	}
	// Joining is mandatory even when a provider ignores cancellation: the
	// physical operation owns its concurrency slot until it actually returns.
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if firstError != nil {
		return nil, firstError
	}
	return observed, nil
}

func creationMetadataCopySeason(response *ports.TVSeasonMetadata, expected ports.SeasonEpisodes) (ports.TVSeasonMetadata, error) {
	if response == nil || !response.Complete || response.SeasonNumber != expected.Season || len(response.Episodes) != expected.Count {
		return ports.TVSeasonMetadata{}, ports.ErrBadResponse
	}
	out := ports.TVSeasonMetadata{SeasonNumber: expected.Season, Complete: true, Episodes: make([]ports.TVEpisodeMetadata, len(response.Episodes))}
	seenNumbers, seenIDs := make(map[int]bool), make(map[int64]bool)
	for i, episode := range response.Episodes {
		if episode.TMDBEpisodeID <= 0 || episode.EpisodeNumber < 1 || episode.EpisodeNumber > 9999 || seenNumbers[episode.EpisodeNumber] || seenIDs[episode.TMDBEpisodeID] {
			return ports.TVSeasonMetadata{}, ports.ErrBadResponse
		}
		seenNumbers[episode.EpisodeNumber], seenIDs[episode.TMDBEpisodeID] = true, true
		if episode.AbsoluteNumber != nil {
			if *episode.AbsoluteNumber <= 0 || *episode.AbsoluteNumber > math.MaxInt32 {
				return ports.TVSeasonMetadata{}, ports.ErrBadResponse
			}
			episode.AbsoluteNumber = new(*episode.AbsoluteNumber)
		}
		if episode.AirDate != nil {
			// time.Time already represents a valid calendar date. Bound the year
			// to the native four-digit date representation, preserving unknown nil.
			if episode.AirDate.Year() < 1 || episode.AirDate.Year() > 9999 {
				return ports.TVSeasonMetadata{}, ports.ErrBadResponse
			}
			date := *episode.AirDate
			if _, err := time.Parse(time.DateOnly, date.Format(time.DateOnly)); err != nil {
				return ports.TVSeasonMetadata{}, ports.ErrBadResponse
			}
			episode.AirDate = &date
		}
		out.Episodes[i] = episode
	}
	sort.Slice(out.Episodes, func(i, j int) bool { return out.Episodes[i].EpisodeNumber < out.Episodes[j].EpisodeNumber })
	return out, nil
}

func creationMetadataProviderError(err error) error {
	// Return only safe sentinels, never a provider's raw error or wrapped text.
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrBadResponse, ports.ErrTimeout} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}
