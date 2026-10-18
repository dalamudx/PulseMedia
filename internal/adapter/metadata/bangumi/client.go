package bangumi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type AnimeItem struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	OrigTitle string  `json:"orig_title"`
	AirDate   string  `json:"air_date"`
	PosterURL string  `json:"poster_url"`
	Rating    float64 `json:"rating"`
}

type DaySchedule struct {
	DayID   int         `json:"day_id"`
	DayName string      `json:"day_name"`
	Items   []AnimeItem `json:"items"`
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
		baseURL = "https://api.bgm.tv"
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// GetCalendar fetches current airing anime calendar for the entire week from Bangumi.
func (c *Client) GetCalendar(ctx context.Context) ([]DaySchedule, error) {
	reqURL := fmt.Sprintf("%s/calendar", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PulseMedia/0.1.0 (https://github.com/dalamudx/PulseMedia)")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	var d []struct {
		Weekday struct {
			ID int    `json:"id"`
			CN string `json:"cn"`
		} `json:"weekday"`
		Items []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			NameCN string `json:"name_cn"`
			Air    string `json:"air_date"`
			Images struct {
				Large  string `json:"large"`
				Common string `json:"common"`
			} `json:"images"`
			Rating struct {
				Score float64 `json:"score"`
			} `json:"rating"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: failed to parse bangumi calendar: %v", ports.ErrBadResponse, err)
	}

	schedules := make([]DaySchedule, len(d))
	for i, day := range d {
		items := make([]AnimeItem, len(day.Items))
		for j, it := range day.Items {
			title := it.NameCN
			if title == "" {
				title = it.Name
			}
			img := it.Images.Large
			if img == "" {
				img = it.Images.Common
			}
			items[j] = AnimeItem{
				ID:        it.ID,
				Title:     title,
				OrigTitle: it.Name,
				AirDate:   it.Air,
				PosterURL: img,
				Rating:    it.Rating.Score,
			}
		}
		schedules[i] = DaySchedule{
			DayID:   day.Weekday.ID,
			DayName: day.Weekday.CN,
			Items:   items,
		}
	}

	return schedules, nil
}
