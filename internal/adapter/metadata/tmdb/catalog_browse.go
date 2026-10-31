package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// searchCatalogMedia is the browse-only search path; identity search keeps its
// separate completeness and cache contracts.
func (c *Client) searchCatalogMedia(ctx context.Context, query, mediaType string, year int) ([]*ports.MovieMetadata, error) {
	if strings.TrimSpace(query) == "" {
		return make([]*ports.MovieMetadata, 0), nil
	}
	switch mediaType {
	case "", "all":
		mediaType = "multi"
	case "movie", "tv", "multi":
	default:
		return nil, ports.ErrRejected
	}
	if year < 0 || (year > 0 && (year < 1800 || year > 9999)) {
		return nil, ports.ErrRejected
	}
	results, _, err := c.searchCatalogPage(ctx, query, mediaType, year)
	return results, err
}

var _ ports.WatchlistIdentityMetadata = (*Client)(nil)

// SearchMediaIdentityCandidates requires complete, fixed-type evidence for identity writes.
func (c *Client) SearchMediaIdentityCandidates(ctx context.Context, query, kind string, year int) (ports.MovieIdentitySearch, error) {
	if err := ctx.Err(); err != nil {
		return ports.MovieIdentitySearch{}, err
	}
	if strings.TrimSpace(query) == "" || len(query) > 512 || (kind != "movie" && kind != "tv") || year < 1800 || year > 9999 {
		return ports.MovieIdentitySearch{}, ports.ErrRejected
	}
	results, raw, err := c.searchCatalogPage(ctx, query, kind, year)
	// A transport may return a response after ignoring cancellation.
	if contextErr := ctx.Err(); contextErr != nil {
		return ports.MovieIdentitySearch{}, contextErr
	}
	if err != nil {
		return ports.MovieIdentitySearch{}, err
	}
	var paging struct {
		Page         *int `json:"page"`
		TotalPages   *int `json:"total_pages"`
		TotalResults *int `json:"total_results"`
	}
	if json.Unmarshal(raw, &paging) != nil {
		return ports.MovieIdentitySearch{}, ports.ErrBadResponse
	}
	complete := false
	if paging.Page != nil && paging.TotalPages != nil && paging.TotalResults != nil && *paging.Page == 1 && *paging.TotalResults == len(results) {
		complete = *paging.TotalPages == 1 || (*paging.TotalPages == 0 && len(results) == 0)
	}
	if err := ctx.Err(); err != nil {
		return ports.MovieIdentitySearch{}, err
	}
	return ports.MovieIdentitySearch{Candidates: results, Complete: complete}, nil
}

// searchCatalogPage shares request budgets and media validation without requiring paging.
func (c *Client) searchCatalogPage(ctx context.Context, query, mediaType string, year int) ([]*ports.MovieMetadata, []byte, error) {
	params := url.Values{"query": {query}, "language": {"zh-CN"}}
	if year > 0 {
		if mediaType == "movie" {
			params.Set("year", strconv.Itoa(year))
		}
		if mediaType == "tv" {
			params.Set("first_air_date_year", strconv.Itoa(year))
		}
	}
	raw, _, err := c.doGet(ctx, c.baseURL+"/search/"+mediaType+"?"+params.Encode(), "")
	if errors.Is(err, errNotModified) {
		return nil, nil, ports.ErrBadResponse
	}
	if err != nil {
		return nil, nil, err
	}
	var response struct {
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Results == nil {
		return nil, nil, ports.ErrBadResponse
	}
	results := make([]*ports.MovieMetadata, 0, len(response.Results))
	for _, item := range response.Results {
		var kind struct {
			MediaType string `json:"media_type"`
		}
		if json.Unmarshal(item, &kind) != nil {
			return nil, nil, ports.ErrBadResponse
		}
		if mediaType == "multi" && kind.MediaType == "person" {
			continue
		}
		resolved := kind.MediaType
		if resolved == "" && mediaType != "multi" {
			resolved = mediaType
		}
		if (resolved != "movie" && resolved != "tv") || (mediaType != "multi" && resolved != mediaType) {
			return nil, nil, ports.ErrBadResponse
		}
		var r struct {
			ID            int64   `json:"id"`
			Title         string  `json:"title"`
			Name          string  `json:"name"`
			OriginalTitle string  `json:"original_title"`
			OriginalName  string  `json:"original_name"`
			ReleaseDate   string  `json:"release_date"`
			FirstAirDate  string  `json:"first_air_date"`
			Overview      string  `json:"overview"`
			PosterPath    *string `json:"poster_path"`
			BackdropPath  *string `json:"backdrop_path"`
		}
		if json.Unmarshal(item, &r) != nil || r.ID <= 0 {
			return nil, nil, ports.ErrBadResponse
		}
		title, original, date := r.Title, r.OriginalTitle, r.ReleaseDate
		if resolved == "tv" {
			title, original, date = r.Name, r.OriginalName, r.FirstAirDate
		}
		itemYear, valid := catalogDateYear(date)
		if strings.TrimSpace(title) == "" || !valid {
			return nil, nil, ports.ErrBadResponse
		}
		// Validate before filtering so malformed media never becomes a false empty.
		if mediaType == "multi" && year > 0 && itemYear != year {
			continue
		}
		results = append(results, &ports.MovieMetadata{
			TMDBID: r.ID, MediaType: resolved, Title: title, OriginalTitle: original,
			Year: itemYear, Overview: r.Overview,
			PosterURL: catalogImageURL(r.PosterPath), BackdropURL: catalogImageURL(r.BackdropPath),
		})
	}
	return results, raw, nil
}

// decodeCatalogTVDetails validates the provider identity and the complete season
// summary. Provider totals and derived totals both exclude specials (season 0).
func decodeCatalogTVDetails(raw []byte, tmdbID int64) (*ports.TVDetails, error) {
	if tmdbID <= 0 {
		return nil, ports.ErrBadResponse
	}
	var d struct {
		ID               int64           `json:"id"`
		MediaType        string          `json:"media_type"`
		Name             string          `json:"name"`
		OriginalName     string          `json:"original_name"`
		FirstAirDate     string          `json:"first_air_date"`
		Overview         string          `json:"overview"`
		PosterPath       *string         `json:"poster_path"`
		BackdropPath     *string         `json:"backdrop_path"`
		VoteAverage      float64         `json:"vote_average"`
		NumberOfSeasons  json.RawMessage `json:"number_of_seasons"`
		NumberOfEpisodes json.RawMessage `json:"number_of_episodes"`
		OriginalLanguage string          `json:"original_language"`
		OriginCountry    []string        `json:"origin_country"`
		Genres           []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Seasons []struct {
			SeasonNumber *int    `json:"season_number"`
			Name         string  `json:"name"`
			EpisodeCount *int    `json:"episode_count"`
			AirDate      string  `json:"air_date"`
			PosterPath   *string `json:"poster_path"`
		} `json:"seasons"`
	}
	if json.Unmarshal(raw, &d) != nil || d.ID != tmdbID || strings.TrimSpace(d.Name) == "" || d.Seasons == nil || (d.MediaType != "" && d.MediaType != "tv") {
		return nil, ports.ErrBadResponse
	}
	year, valid := catalogDateYear(d.FirstAirDate)
	if !valid {
		return nil, ports.ErrBadResponse
	}
	if !validClassificationOrigin(d.OriginalLanguage, d.OriginCountry) {
		return nil, ports.ErrBadResponse
	}
	tv := &ports.TVDetails{
		TMDBID: d.ID, Title: d.Name, OriginalTitle: d.OriginalName, Year: year,
		Overview: d.Overview, Rating: d.VoteAverage,
		PosterURL: catalogImageURL(d.PosterPath), BackdropURL: catalogImageURL(d.BackdropPath),
		Genres: make([]string, 0, len(d.Genres)), Seasons: make([]ports.TVSeasonDetails, 0, len(d.Seasons)),
	}
	tv.OriginalLanguage, tv.Countries = d.OriginalLanguage, append([]string(nil), d.OriginCountry...)
	for _, genre := range d.Genres {
		tv.Genres = append(tv.Genres, genre.Name)
	}
	seen := make(map[int]bool, len(d.Seasons))
	for _, s := range d.Seasons {
		if s.SeasonNumber == nil || s.EpisodeCount == nil || *s.SeasonNumber < 0 || *s.EpisodeCount < 0 || seen[*s.SeasonNumber] {
			return nil, ports.ErrBadResponse
		}
		if _, valid := catalogDateYear(s.AirDate); !valid {
			return nil, ports.ErrBadResponse
		}
		number, count := *s.SeasonNumber, *s.EpisodeCount
		seen[number] = true
		if number > 0 {
			if count > int(^uint(0)>>1)-tv.EpisodesCount {
				return nil, ports.ErrBadResponse
			}
			tv.SeasonsCount++
			tv.EpisodesCount += count
		}
		name := s.Name
		if strings.TrimSpace(name) == "" {
			name = "特别篇"
			if number > 0 {
				name = fmt.Sprintf("第 %d 季", number)
			}
		}
		tv.Seasons = append(tv.Seasons, ports.TVSeasonDetails{SeasonNumber: number, Name: name, EpisodeCount: count, AirDate: s.AirDate, PosterURL: catalogImageURL(s.PosterPath)})
	}
	if !catalogCountMatches(d.NumberOfSeasons, tv.SeasonsCount) || !catalogCountMatches(d.NumberOfEpisodes, tv.EpisodesCount) {
		return nil, ports.ErrBadResponse
	}
	return tv, nil
}

func catalogCountMatches(raw json.RawMessage, derived int) bool {
	if len(raw) == 0 {
		return true
	}
	var count *int
	return json.Unmarshal(raw, &count) == nil && count != nil && *count >= 0 && *count == derived
}

func catalogDateYear(date string) (int, bool) {
	if date == "" {
		return 0, true
	}
	if len(date) != len("2006-01-02") {
		return 0, false
	}
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Year() < 1800 {
		return 0, false
	}
	return parsed.Year(), true
}

func catalogImageURL(path *string) string {
	if path == nil {
		return ""
	}
	return imageBaseURL + *path
}
