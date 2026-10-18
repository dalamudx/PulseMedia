package douban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
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

type Client struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if baseURL == "" {
		baseURL = "https://frodo.douban.com/api/v2"
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// GetHotMovies fetches current trending movies from Douban.
func (c *Client) GetHotMovies(ctx context.Context) ([]Item, error) {
	reqURL := fmt.Sprintf("%s/movie/hot_gaia?area=全部&sort=recommend", c.baseURL)
	return c.fetchItems(ctx, reqURL, "movie")
}

// GetHotTVShows fetches trending TV shows from Douban.
func (c *Client) GetHotTVShows(ctx context.Context) ([]Item, error) {
	reqURL := fmt.Sprintf("%s/tv/hot_gaia?area=全部&sort=recommend", c.baseURL)
	return c.fetchItems(ctx, reqURL, "tv")
}

// GetTop250 fetches the famous Douban Top 250 movies list.
func (c *Client) GetTop250(ctx context.Context, start, count int) ([]Item, error) {
	if count <= 0 {
		count = 20
	}
	reqURL := fmt.Sprintf("%s/movie/top250?start=%d&count=%d", c.baseURL, start, count)
	return c.fetchItems(ctx, reqURL, "movie")
}

func (c *Client) fetchItems(ctx context.Context, reqURL, mediaType string) ([]Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "api-client/1.0.0 com.douban.frodo")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ports.NewRateLimitError(5*time.Second, "douban rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	var d struct {
		SelectedCollections []struct {
			Items []struct {
				ID            string `json:"id"`
				Title         string `json:"title"`
				OriginalTitle string `json:"original_title"`
				Year          string `json:"year"`
				Rating        struct {
					Value float64 `json:"value"`
				} `json:"rating"`
				Pic struct {
					Normal string `json:"normal"`
					Large  string `json:"large"`
				} `json:"pic"`
			} `json:"items"`
		} `json:"selected_collections"`
		Subjects []struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			OriginalTitle string `json:"original_title"`
			Year          string `json:"year"`
			Rating        struct {
				Value float64 `json:"value"`
			} `json:"rating"`
			Pic struct {
				Normal string `json:"normal"`
				Large  string `json:"large"`
			} `json:"pic"`
		} `json:"subjects"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: failed to parse douban json: %v", ports.ErrBadResponse, err)
	}

	items := make([]Item, 0)

	// Parse subjects
	for _, s := range d.Subjects {
		y, _ := strconv.Atoi(s.Year)
		pic := s.Pic.Large
		if pic == "" {
			pic = s.Pic.Normal
		}
		items = append(items, Item{
			ID:            s.ID,
			Title:         s.Title,
			OriginalTitle: s.OriginalTitle,
			Year:          y,
			Rating:        s.Rating.Value,
			PosterURL:     pic,
			MediaType:     mediaType,
		})
	}

	// Parse selected collections
	for _, col := range d.SelectedCollections {
		for _, s := range col.Items {
			y, _ := strconv.Atoi(s.Year)
			pic := s.Pic.Large
			if pic == "" {
				pic = s.Pic.Normal
			}
			items = append(items, Item{
				ID:            s.ID,
				Title:         s.Title,
				OriginalTitle: s.OriginalTitle,
				Year:          y,
				Rating:        s.Rating.Value,
				PosterURL:     pic,
				MediaType:     mediaType,
			})
		}
	}

	return items, nil
}
