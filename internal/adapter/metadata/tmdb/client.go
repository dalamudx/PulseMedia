package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	defaultBaseURL  = "https://api.themoviedb.org/3"
	imageBaseURL    = "https://image.tmdb.org/t/p/w500"
	detailsCacheTTL = 24 * time.Hour
)

var _ ports.MetadataProvider = (*Client)(nil)

type cacheEntry struct {
	data      *ports.MovieMetadata
	etag      string
	expiresAt time.Time
	revision  uint64
}

// Client implements ports.MetadataProvider for TMDB.
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	clock      ports.Clock
	budget     ports.MetadataRequestBudget

	mu            sync.RWMutex
	cache         map[string]cacheEntry
	cacheRevision uint64
}

type Option func(*Client)

func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.httpClient = client
	}
}

func WithBaseURL(u string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimRight(u, "/")
	}
}

func WithClock(clock ports.Clock) Option {
	return func(c *Client) {
		c.clock = clock
	}
}

func WithRequestBudget(b ports.MetadataRequestBudget) Option {
	return func(c *Client) { c.budget = b }
}

func NewClient(apiKey string, opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		cache:      make(map[string]cacheEntry),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.budget == nil {
		c.budget = budget.NewMetadataBudget(c.clock)
	}
	return c
}

// SearchMovies searches TMDB for movies matching query and optional year.
func (c *Client) SearchMovies(ctx context.Context, query string, year int) ([]*ports.MovieMetadata, error) {
	results, _, err := c.searchMovies(ctx, query, year)
	return results, err
}

// SearchMovieIdentityCandidates keeps incomplete browse pages out of identity writes.
func (c *Client) SearchMovieIdentityCandidates(ctx context.Context, query string, year int) (ports.MovieIdentitySearch, error) {
	results, complete, err := c.searchMovies(ctx, query, year)
	if err != nil {
		return ports.MovieIdentitySearch{}, err
	}
	return ports.MovieIdentitySearch{Candidates: results, Complete: complete}, nil
}

func (c *Client) searchMovies(ctx context.Context, query string, year int) ([]*ports.MovieMetadata, bool, error) {
	if query == "" {
		return nil, true, nil
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("language", "zh-CN")
	if year > 0 {
		params.Set("year", strconv.Itoa(year))
	}
	reqURL := fmt.Sprintf("%s/search/movie?%s", c.baseURL, params.Encode())
	respBytes, _, err := c.doGet(ctx, reqURL, "")
	if errors.Is(err, errNotModified) {
		return nil, false, fmt.Errorf("%w: unconditional movie search returned not modified", ports.ErrBadResponse)
	}
	if err != nil {
		return nil, false, err
	}
	var searchResp struct {
		Page         *int `json:"page"`
		TotalPages   *int `json:"total_pages"`
		TotalResults *int `json:"total_results"`
		Results      []struct {
			ID            int64   `json:"id"`
			MediaType     string  `json:"media_type"`
			Title         string  `json:"title"`
			OriginalTitle string  `json:"original_title"`
			ReleaseDate   string  `json:"release_date"`
			Overview      string  `json:"overview"`
			PosterPath    *string `json:"poster_path"`
			BackdropPath  *string `json:"backdrop_path"`
		} `json:"results"`
	}
	if err := json.Unmarshal(respBytes, &searchResp); err != nil {
		return nil, false, fmt.Errorf("%w: invalid movie search response", ports.ErrBadResponse)
	}
	if searchResp.Results == nil {
		return nil, false, fmt.Errorf("%w: missing movie search results", ports.ErrBadResponse)
	}
	results := make([]*ports.MovieMetadata, 0, len(searchResp.Results))
	for _, r := range searchResp.Results {
		if r.ID < 1 || (r.MediaType != "" && r.MediaType != "movie") {
			return nil, false, fmt.Errorf("%w: invalid movie search identity", ports.ErrBadResponse)
		}
		releaseYear := 0
		if r.ReleaseDate != "" {
			date, err := time.Parse("2006-01-02", r.ReleaseDate)
			if err != nil {
				return nil, false, fmt.Errorf("%w: invalid movie search release date", ports.ErrBadResponse)
			}
			releaseYear = date.Year()
		}
		m := &ports.MovieMetadata{TMDBID: r.ID, Title: r.Title, OriginalTitle: r.OriginalTitle, MediaType: "movie", Year: releaseYear, Overview: r.Overview}
		if r.PosterPath != nil {
			m.PosterURL = imageBaseURL + *r.PosterPath
		}
		if r.BackdropPath != nil {
			m.BackdropURL = imageBaseURL + *r.BackdropPath
		}
		results = append(results, m)
	}
	complete := false
	if searchResp.Page != nil && searchResp.TotalPages != nil && searchResp.TotalResults != nil && *searchResp.Page == 1 && *searchResp.TotalResults == len(results) {
		complete = (*searchResp.TotalPages == 1) || (*searchResp.TotalPages == 0 && len(results) == 0)
	}
	return results, complete, nil
}

// GetMovieDetails retrieves movie details with language fallback (zh-CN -> en-US) and ETag caching.
func (c *Client) GetMovieDetails(ctx context.Context, tmdbID int64, language string) (*ports.MovieMetadata, error) {
	if tmdbID <= 0 {
		return nil, ports.ErrNotFound
	}
	if language == "" {
		language = "zh-CN"
	}
	m, err := c.getMovieLanguageDetails(ctx, tmdbID, language)
	if err != nil {
		return nil, err
	}
	// Each language retains its own raw representation and validator. Compose
	// fallback only in the caller's copy, never under the primary language ETag.
	if m.Overview == "" && language != "en-US" {
		english, fallbackErr := c.getMovieLanguageDetails(ctx, tmdbID, "en-US")
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if fallbackErr == nil && english != nil {
			m.Overview = english.Overview
		}
	}
	return m, nil
}

func cloneMovieMetadata(m *ports.MovieMetadata) *ports.MovieMetadata {
	if m == nil {
		return nil
	}
	copy := *m
	copy.Genres = append([]string(nil), m.Genres...)
	return &copy
}

func (c *Client) getMovieLanguageDetails(ctx context.Context, tmdbID int64, language string) (*ports.MovieMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("movie:%d:%s", tmdbID, language)
	c.mu.Lock()
	cached, found := c.cache[key]
	if found && cached.data != nil && c.now().Before(cached.expiresAt) {
		c.mu.Unlock()
		return cloneMovieMetadata(cached.data), nil
	}
	c.cacheRevision++
	revision := c.cacheRevision
	cached.revision = revision
	c.cache[key] = cached
	c.mu.Unlock()
	publish := func(data *ports.MovieMetadata, etag string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.cache[key].revision == revision {
			c.cache[key] = cacheEntry{data: data, etag: etag, expiresAt: c.now().Add(detailsCacheTTL), revision: revision}
		}
	}
	reqURL := fmt.Sprintf("%s/movie/%d?language=%s", c.baseURL, tmdbID, url.QueryEscape(language))
	body, etag, err := c.doGet(ctx, reqURL, cached.etag)
	if err != nil {
		if !errors.Is(err, errNotModified) {
			return nil, err
		}
		if !found || cached.data == nil || cached.etag == "" {
			return nil, ports.ErrBadResponse
		}
		if etag == "" {
			etag = cached.etag
		}
		publish(cached.data, etag)
		return cloneMovieMetadata(cached.data), nil
	}
	var d struct {
		ID            int64   `json:"id"`
		IMDbID        *string `json:"imdb_id"`
		Title         string  `json:"title"`
		OriginalTitle string  `json:"original_title"`
		ReleaseDate   string  `json:"release_date"`
		Overview      string  `json:"overview"`
		PosterPath    *string `json:"poster_path"`
		BackdropPath  *string `json:"backdrop_path"`
		Runtime       int     `json:"runtime"`
		VoteAverage   float64 `json:"vote_average"`
		Genres        []struct {
			Name string `json:"name"`
		} `json:"genres"`
	}
	if err := json.Unmarshal(body, &d); err != nil || d.ID != tmdbID {
		return nil, ports.ErrBadResponse
	}
	genres := make([]string, len(d.Genres))
	for i, g := range d.Genres {
		genres[i] = g.Name
	}
	m := &ports.MovieMetadata{MediaType: "movie", TMDBID: d.ID, Title: d.Title, OriginalTitle: d.OriginalTitle, Overview: d.Overview, Year: parseYearFromDate(d.ReleaseDate), Genres: genres, Runtime: d.Runtime, Rating: d.VoteAverage}
	if d.IMDbID != nil {
		m.IMDbID = *d.IMDbID
	}
	if d.PosterPath != nil {
		m.PosterURL = imageBaseURL + *d.PosterPath
	}
	if d.BackdropPath != nil {
		m.BackdropURL = imageBaseURL + *d.BackdropPath
	}
	publish(m, etag)
	return cloneMovieMetadata(m), nil
}

// FindMovieByExternalID resolves external IDs (like imdb:tt0137523) to TMDB movie.
func (c *Client) FindMovieByExternalID(ctx context.Context, namespace, externalID string) (*ports.MovieMetadata, error) {
	namespace = strings.ToLower(namespace)
	externalID = strings.ToLower(externalID)
	var sourceParam string
	switch namespace {
	case "imdb":
		if !validIMDbExternalID(externalID) {
			return nil, fmt.Errorf("%w: invalid IMDb identity", ports.ErrRejected)
		}
		sourceParam = "imdb_id"
	case "tvdb":
		id, err := strconv.ParseInt(externalID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != externalID {
			return nil, fmt.Errorf("%w: invalid TVDB identity", ports.ErrRejected)
		}
		sourceParam = "tvdb_id"
	default:
		return nil, fmt.Errorf("%w: unsupported external namespace", ports.ErrRejected)
	}
	reqURL := fmt.Sprintf("%s/find/%s?external_source=%s&language=zh-CN", c.baseURL, url.PathEscape(externalID), sourceParam)
	respBytes, _, err := c.doGet(ctx, reqURL, "")
	if errors.Is(err, errNotModified) {
		return nil, fmt.Errorf("%w: unexpected Find validator response", ports.ErrBadResponse)
	}
	if err != nil {
		return nil, err
	}
	var found struct {
		MovieResults []struct {
			ID int64 `json:"id"`
		} `json:"movie_results"`
	}
	if err := json.Unmarshal(respBytes, &found); err != nil || found.MovieResults == nil {
		return nil, fmt.Errorf("%w: invalid Find response", ports.ErrBadResponse)
	}
	if len(found.MovieResults) == 0 {
		return nil, ports.ErrNotFound
	}
	// A direct external identity cannot authorize selecting a ranked first hit.
	if len(found.MovieResults) != 1 || found.MovieResults[0].ID <= 0 {
		return nil, fmt.Errorf("%w: non-unique or invalid Find identity", ports.ErrBadResponse)
	}
	metadata, err := c.GetMovieDetails(ctx, found.MovieResults[0].ID, "zh-CN")
	if err != nil {
		return nil, err
	}
	if namespace == "imdb" && !strings.EqualFold(metadata.IMDbID, externalID) {
		return nil, fmt.Errorf("%w: Find identity does not match movie details", ports.ErrBadResponse)
	}
	return metadata, nil
}

func validIMDbExternalID(id string) bool {
	if len(id) < 9 || len(id) > 12 || !strings.HasPrefix(id, "tt") {
		return false
	}
	for _, b := range id[2:] {
		if b < '0' || b > '9' {
			return false
		}
	}
	numeric, err := strconv.ParseInt(id[2:], 10, 64)
	return err == nil && numeric > 0
}

var errNotModified = errors.New("304 not modified")

func (c *Client) doGet(ctx context.Context, rawURL, ifNoneMatch string) ([]byte, string, error) {
	if c.httpClient.Timeout > 0 {
		bounded, cancel := context.WithTimeout(ctx, c.httpClient.Timeout)
		defer cancel()
		ctx = bounded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", ports.ErrBadResponse
	}

	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		if strings.HasPrefix(c.apiKey, "ey") || len(c.apiKey) > 50 {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		} else {
			q := req.URL.Query()
			q.Set("api_key", c.apiKey)
			req.URL.RawQuery = q.Encode()
		}
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	release, err := c.budget.Acquire(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "", ports.ErrTimeout
		}
		return nil, "", err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, "", ports.ErrTimeout
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", ports.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.Header.Get("ETag"), errNotModified
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		c.budget.RecordRateLimit(retryAfter)
		return nil, "", ports.NewRateLimitError(retryAfter, "TMDB rate limit exceeded")
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, "", ports.ErrAuthRequired
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ports.ErrNotFound
	}

	if resp.StatusCode >= 500 {
		return nil, "", fmt.Errorf("%w: TMDB server error (status %d)", ports.ErrUnavailable, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: unexpected HTTP status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	rawData, e := io.ReadAll(io.LimitReader(resp.Body, (5<<20)+1))
	if e != nil || len(rawData) > 5<<20 || !json.Valid(rawData) {
		return nil, "", ports.ErrBadResponse
	}
	return rawData, resp.Header.Get("ETag"), nil
}

func (c *Client) now() time.Time {
	if c.clock != nil {
		return c.clock.Now()
	}
	return time.Now().UTC()
}

func parseYearFromDate(dateStr string) int {
	if len(dateStr) >= 4 {
		year, err := strconv.Atoi(dateStr[:4])
		if err == nil {
			return year
		}
	}
	return 0
}

func parseRetryAfter(headerVal string) time.Duration {
	if headerVal == "" {
		return 5 * time.Second
	}
	if secs, err := strconv.Atoi(headerVal); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 5 * time.Second
}

// SearchMedia searches TMDB for movies, TV shows, or both.
func (c *Client) SearchMedia(ctx context.Context, query, mediaType string, year int) ([]*ports.MovieMetadata, error) {
	if query == "" {
		return nil, nil
	}
	if mediaType == "movie" {
		return c.SearchMovies(ctx, query, year)
	}

	params := url.Values{}
	params.Set("query", query)
	params.Set("language", "zh-CN")

	endpoint := "search/multi"
	if mediaType == "tv" {
		endpoint = "search/tv"
	}

	reqURL := fmt.Sprintf("%s/%s?%s", c.baseURL, endpoint, params.Encode())
	respBytes, _, err := c.doGet(ctx, reqURL, "")
	if err != nil {
		return nil, err
	}

	var searchResp struct {
		Results []struct {
			ID            int64   `json:"id"`
			MediaType     string  `json:"media_type"`
			Title         string  `json:"title"`
			Name          string  `json:"name"`
			OriginalTitle string  `json:"original_title"`
			OriginalName  string  `json:"original_name"`
			ReleaseDate   string  `json:"release_date"`
			FirstAirDate  string  `json:"first_air_date"`
			Overview      string  `json:"overview"`
			PosterPath    *string `json:"poster_path"`
			BackdropPath  *string `json:"backdrop_path"`
		} `json:"results"`
	}

	if err := json.Unmarshal(respBytes, &searchResp); err != nil {
		return nil, fmt.Errorf("%w: failed to parse media search results: %v", ports.ErrBadResponse, err)
	}

	results := make([]*ports.MovieMetadata, 0, len(searchResp.Results))
	for _, r := range searchResp.Results {
		resolvedType := r.MediaType
		if resolvedType == "" {
			resolvedType = mediaType
			if resolvedType == "" {
				resolvedType = "movie"
			}
		}
		if resolvedType != "movie" && resolvedType != "tv" {
			continue // skip person or other non-media items
		}

		title := r.Title
		origTitle := r.OriginalTitle
		dateStr := r.ReleaseDate
		if resolvedType == "tv" {
			title = r.Name
			origTitle = r.OriginalName
			dateStr = r.FirstAirDate
		}

		m := &ports.MovieMetadata{
			TMDBID:        r.ID,
			MediaType:     resolvedType,
			Title:         title,
			OriginalTitle: origTitle,
			Overview:      r.Overview,
			Year:          parseYearFromDate(dateStr),
		}
		if r.PosterPath != nil {
			m.PosterURL = imageBaseURL + *r.PosterPath
		}
		if r.BackdropPath != nil {
			m.BackdropURL = imageBaseURL + *r.BackdropPath
		}
		results = append(results, m)
	}

	return results, nil
}

// GetTVDetails retrieves TV show details including seasons and episode counts.
func (c *Client) GetTVDetails(ctx context.Context, tmdbID int64, language string) (*ports.TVDetails, error) {
	if tmdbID <= 0 {
		return nil, ports.ErrNotFound
	}
	if language == "" {
		language = "zh-CN"
	}

	reqURL := fmt.Sprintf("%s/tv/%d?language=%s", c.baseURL, tmdbID, url.QueryEscape(language))
	respBytes, _, err := c.doGet(ctx, reqURL, "")
	if err != nil {
		return nil, err
	}

	var d struct {
		ID           int64   `json:"id"`
		Name         string  `json:"name"`
		OriginalName string  `json:"original_name"`
		FirstAirDate string  `json:"first_air_date"`
		Overview     string  `json:"overview"`
		PosterPath   *string `json:"poster_path"`
		BackdropPath *string `json:"backdrop_path"`
		VoteAverage  float64 `json:"vote_average"`
		Genres       []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Seasons []struct {
			SeasonNumber int     `json:"season_number"`
			Name         string  `json:"name"`
			EpisodeCount int     `json:"episode_count"`
			AirDate      string  `json:"air_date"`
			PosterPath   *string `json:"poster_path"`
		} `json:"seasons"`
	}

	if err := json.Unmarshal(respBytes, &d); err != nil {
		return nil, fmt.Errorf("%w: failed to parse tv details: %v", ports.ErrBadResponse, err)
	}

	genres := make([]string, len(d.Genres))
	for i, g := range d.Genres {
		genres[i] = g.Name
	}

	seasons := make([]ports.TVSeasonDetails, 0, len(d.Seasons))
	for _, s := range d.Seasons {
		var sPoster string
		if s.PosterPath != nil {
			sPoster = imageBaseURL + *s.PosterPath
		}
		sName := s.Name
		if sName == "" {
			if s.SeasonNumber == 0 {
				sName = "特别篇"
			} else {
				sName = fmt.Sprintf("第 %d 季", s.SeasonNumber)
			}
		}
		seasons = append(seasons, ports.TVSeasonDetails{
			SeasonNumber: s.SeasonNumber,
			Name:         sName,
			EpisodeCount: s.EpisodeCount,
			AirDate:      s.AirDate,
			PosterURL:    sPoster,
		})
	}

	tv := &ports.TVDetails{
		TMDBID:        d.ID,
		Title:         d.Name,
		OriginalTitle: d.OriginalName,
		Year:          parseYearFromDate(d.FirstAirDate),
		Overview:      d.Overview,
		Rating:        d.VoteAverage,
		Genres:        genres,
		Seasons:       seasons,
	}
	if d.PosterPath != nil {
		tv.PosterURL = imageBaseURL + *d.PosterPath
	}
	if d.BackdropPath != nil {
		tv.BackdropURL = imageBaseURL + *d.BackdropPath
	}
	return tv, nil
}

var tmdbGenreMap = map[int]string{
	28:    "动作",
	12:    "冒险",
	16:    "动画",
	35:    "喜剧",
	80:    "犯罪",
	99:    "纪录",
	18:    "剧情",
	10751: "家庭",
	14:    "奇幻",
	36:    "历史",
	27:    "恐怖",
	10402: "音乐",
	9648:  "悬疑",
	10749: "爱情",
	878:   "科幻",
	10770: "电视电影",
	53:    "惊悚",
	10752: "战争",
	37:    "西部",
	10759: "动作",
	10765: "科幻",
	10768: "战争",
}

// GetDiscoverFeed fetches curated feeds (trending, movie, tv, top_rated) from TMDB.
func (c *Client) GetDiscoverFeed(ctx context.Context, category string, page int) ([]*ports.MovieMetadata, error) {
	if page <= 0 {
		page = 1
	}

	var endpoint string
	var defaultType string

	switch category {
	case "movie", "movies", "popular_movies":
		endpoint = "movie/popular"
		defaultType = "movie"
	case "tv", "popular_tv":
		endpoint = "tv/popular"
		defaultType = "tv"
	case "top_rated":
		endpoint = "movie/top_rated"
		defaultType = "movie"
	default:
		endpoint = "trending/all/day"
		defaultType = "movie"
	}

	reqURL := fmt.Sprintf("%s/%s?language=zh-CN&page=%d", c.baseURL, endpoint, page)
	respBytes, _, err := c.doGet(ctx, reqURL, "")
	if err != nil {
		return nil, err
	}

	var d struct {
		Results []struct {
			ID           int64   `json:"id"`
			MediaType    string  `json:"media_type"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			Overview     string  `json:"overview"`
			PosterPath   *string `json:"poster_path"`
			BackdropPath *string `json:"backdrop_path"`
			VoteAverage  float64 `json:"vote_average"`
			GenreIDs     []int   `json:"genre_ids"`
		} `json:"results"`
	}

	if err := json.Unmarshal(respBytes, &d); err != nil {
		return nil, fmt.Errorf("%w: failed to parse discover feed: %v", ports.ErrBadResponse, err)
	}

	results := make([]*ports.MovieMetadata, 0, len(d.Results))
	for _, r := range d.Results {
		mType := r.MediaType
		if mType == "" {
			mType = defaultType
		}
		if mType != "movie" && mType != "tv" {
			continue
		}

		title := r.Title
		dateStr := r.ReleaseDate
		if mType == "tv" {
			title = r.Name
			dateStr = r.FirstAirDate
		}

		var poster, backdrop string
		if r.PosterPath != nil {
			poster = imageBaseURL + *r.PosterPath
		}
		if r.BackdropPath != nil {
			backdrop = imageBaseURL + *r.BackdropPath
		}

		genres := make([]string, 0, len(r.GenreIDs))
		for _, gid := range r.GenreIDs {
			if gName, ok := tmdbGenreMap[gid]; ok {
				genres = append(genres, gName)
			}
		}

		results = append(results, &ports.MovieMetadata{
			TMDBID:      r.ID,
			MediaType:   mType,
			Title:       title,
			Year:        parseYearFromDate(dateStr),
			Overview:    r.Overview,
			PosterURL:   poster,
			BackdropURL: backdrop,
			Rating:      r.VoteAverage,
			Genres:      genres,
		})
	}

	return results, nil
}
