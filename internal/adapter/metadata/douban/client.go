package douban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	defaultBaseURL = "https://frodo.douban.com/api/v2"
	maxBodyBytes   = 2 << 20
	maxItems       = 100
	maxCount       = 50
	maxTextRunes   = 200
)

var (
	idPattern     = regexp.MustCompile(`^[0-9]{1,20}$`)
	posterHostRe  = regexp.MustCompile(`^img[0-9]*\.doubanio\.com$`)
	errBadPayload = fmt.Errorf("%w: invalid douban payload", ports.ErrBadResponse)
)

type Item struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title,omitempty"`
	Year          int     `json:"year,omitempty"`
	Rating        float64 `json:"rating,omitempty"`
	PosterURL     string  `json:"poster_url,omitempty"`
	MediaType     string  `json:"media_type"` // "movie" or "tv"
}

var _ ports.DoubanRankingProvider = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient copies the supplied client so redirects and cookies can be
// disabled without mutating the caller's transport policy.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	hc := http.Client{Timeout: 10 * time.Second}
	if httpClient != nil {
		hc = *httpClient
	}
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{httpClient: &hc, baseURL: strings.TrimRight(baseURL, "/")}
}

// GetHotMovies fetches current trending movies from Douban.
func (c *Client) GetHotMovies(ctx context.Context) ([]Item, error) {
	return c.legacy(ctx, ports.DoubanListHotMovie, 0, maxCount)
}

// GetHotTVShows fetches trending TV shows from Douban.
func (c *Client) GetHotTVShows(ctx context.Context) ([]Item, error) {
	return c.legacy(ctx, ports.DoubanListHotTV, 0, maxCount)
}

// GetTop250 fetches the Douban Top 250 movies list.
func (c *Client) GetTop250(ctx context.Context, start, count int) ([]Item, error) {
	if count <= 0 {
		count = 20
	}
	return c.legacy(ctx, ports.DoubanListTop250, start, count)
}

func (c *Client) legacy(ctx context.Context, list string, start, count int) ([]Item, error) {
	ranked, err := c.GetRanking(ctx, list, start, count)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(ranked))
	for _, r := range ranked {
		items = append(items, Item{ID: r.DoubanID, Title: r.Title, OriginalTitle: r.OriginalTitle, Year: r.Year, Rating: r.Rating, PosterURL: r.PosterURL, MediaType: r.MediaType})
	}
	return items, nil
}

// GetRanking reads one explicit list window. Hot lists are single-window
// (start must be 0); at most count validated items are returned.
func (c *Client) GetRanking(ctx context.Context, list string, start, count int) ([]ports.DoubanRankingItem, error) {
	if start < 0 || count < 1 || count > maxCount {
		return nil, ports.ErrRejected
	}
	query := url.Values{}
	var path, kind string
	switch list {
	case ports.DoubanListHotMovie, ports.DoubanListHotTV:
		if start != 0 {
			return nil, ports.ErrRejected
		}
		kind = "movie"
		path = "/movie/hot_gaia"
		if list == ports.DoubanListHotTV {
			kind, path = "tv", "/tv/hot_gaia"
		}
		query.Set("area", "全部")
		query.Set("sort", "recommend")
	case ports.DoubanListWeeklyBest:
		kind, path = "movie", "/subject_collection/movie_weekly_best/items"
	case ports.DoubanListTop250:
		kind, path = "movie", "/movie/top250"
	default:
		return nil, ports.ErrRejected
	}
	if list == ports.DoubanListWeeklyBest || list == ports.DoubanListTop250 {
		query.Set("start", strconv.Itoa(start))
		query.Set("count", strconv.Itoa(count))
	}
	items, err := c.fetch(ctx, c.baseURL+path+"?"+query.Encode(), kind)
	if err != nil {
		return nil, err
	}
	if len(items) > count {
		items = items[:count]
	}
	return items, nil
}

type rawItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Year          string `json:"year"`
	Type          string `json:"type"`
	Rating        struct {
		Value float64 `json:"value"`
	} `json:"rating"`
	Pic struct {
		Normal string `json:"normal"`
		Large  string `json:"large"`
	} `json:"pic"`
}

func (c *Client) fetch(ctx context.Context, reqURL, kind string) ([]ports.DoubanRankingItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, ports.ErrRejected
	}
	req.Header.Set("User-Agent", "api-client/1.0.0 com.douban.frodo")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return nil, ports.ErrTimeout
		}
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ports.NewRateLimitError(5*time.Second, "douban rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ports.ErrUnavailable
	}
	if len(raw) > maxBodyBytes {
		return nil, errBadPayload
	}
	var d struct {
		Subjects            *[]rawItem `json:"subjects"`
		CollectionItems     *[]rawItem `json:"subject_collection_items"`
		SelectedCollections *[]struct {
			Items []rawItem `json:"items"`
		} `json:"selected_collections"`
	}
	if json.Unmarshal(raw, &d) != nil || (d.Subjects == nil && d.CollectionItems == nil && d.SelectedCollections == nil) {
		return nil, errBadPayload
	}
	var all []rawItem
	if d.Subjects != nil {
		all = append(all, *d.Subjects...)
	}
	if d.CollectionItems != nil {
		all = append(all, *d.CollectionItems...)
	}
	if d.SelectedCollections != nil {
		for _, col := range *d.SelectedCollections {
			all = append(all, col.Items...)
		}
	}
	if len(all) > maxItems {
		return nil, errBadPayload
	}
	seen := make(map[string]bool, len(all))
	items := make([]ports.DoubanRankingItem, 0, len(all))
	for _, r := range all {
		item, err := convert(r, kind)
		if err != nil {
			return nil, err
		}
		if seen[item.DoubanID] {
			continue
		}
		seen[item.DoubanID] = true
		items = append(items, item)
	}
	return items, nil
}

func convert(r rawItem, kind string) (ports.DoubanRankingItem, error) {
	var zero ports.DoubanRankingItem
	title, original := strings.TrimSpace(r.Title), strings.TrimSpace(r.OriginalTitle)
	if !idPattern.MatchString(r.ID) || title == "" || utf8.RuneCountInString(title) > maxTextRunes || utf8.RuneCountInString(original) > maxTextRunes || (r.Type != "" && r.Type != kind) {
		return zero, errBadPayload
	}
	if v := r.Rating.Value; v < 0 || v > 10 {
		return zero, errBadPayload
	}
	year := 0
	if r.Year != "" {
		y, err := strconv.Atoi(r.Year)
		if err != nil || y < 1800 || y > 9999 {
			return zero, errBadPayload
		}
		year = y
	}
	return ports.DoubanRankingItem{
		DoubanID: r.ID, Title: title, OriginalTitle: original, Year: year,
		Rating: r.Rating.Value, PosterURL: posterURL(r.Pic.Large, r.Pic.Normal), MediaType: kind,
	}, nil
}

// posterURL keeps only HTTPS images on Douban's image hosts; anything else is
// dropped rather than rendered or fetched later.
func posterURL(candidates ...string) string {
	for _, raw := range candidates {
		u, err := url.Parse(raw)
		if raw != "" && err == nil && u.Scheme == "https" && u.User == nil && posterHostRe.MatchString(u.Hostname()) && u.Port() == "" && u.Fragment == "" {
			return raw
		}
	}
	return ""
}
