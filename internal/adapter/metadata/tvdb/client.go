package tvdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

func NewClient(apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    "https://api4.thetvdb.com/v4",
		apiKey:     apiKey,
	}
}

// GetSeriesDetails retrieves series details and seasons from TheTVDB v4.
func (c *Client) GetSeriesDetails(ctx context.Context, seriesID int64) (*ports.TVDetails, error) {
	if seriesID <= 0 {
		return nil, errors.New("invalid seriesID")
	}

	reqURL := fmt.Sprintf("%s/series/%d/extended", c.baseURL, seriesID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ports.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	var d struct {
		Data struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			Year         string `json:"year"`
			Overview     string `json:"overview"`
			Image        string `json:"image"`
			Score        float64 `json:"score"`
			Seasons      []struct {
				ID           int64  `json:"id"`
				Number       int    `json:"number"`
				Name         string `json:"name"`
				Image        string `json:"image"`
			} `json:"seasons"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: failed to parse tvdb response: %v", ports.ErrBadResponse, err)
	}

	y, _ := strconv.Atoi(d.Data.Year)
	seasons := make([]ports.TVSeasonDetails, len(d.Data.Seasons))
	for i, s := range d.Data.Seasons {
		sName := s.Name
		if sName == "" {
			sName = fmt.Sprintf("第 %d 季", s.Number)
		}
		seasons[i] = ports.TVSeasonDetails{
			SeasonNumber: s.Number,
			Name:         sName,
			EpisodeCount: 10, // default estimate if not expanded
			PosterURL:    s.Image,
		}
	}

	return &ports.TVDetails{
		TMDBID:    d.Data.ID,
		Title:     d.Data.Name,
		Year:      y,
		Overview:  d.Data.Overview,
		PosterURL: d.Data.Image,
		Rating:    d.Data.Score,
		Seasons:   seasons,
	}, nil
}
