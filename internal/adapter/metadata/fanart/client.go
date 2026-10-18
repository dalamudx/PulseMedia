package fanart

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

type ArtworkImage struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Lang     string `json:"lang"`
	Likes    string `json:"likes"`
}

type MovieArtworks struct {
	ClearLogo string `json:"clear_logo"`
	Banner    string `json:"banner"`
	DiscArt   string `json:"disc_art"`
}

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
		baseURL:    "https://webservice.fanart.tv/v3",
		apiKey:     apiKey,
	}
}

// GetMovieArtworks retrieves transparent clearlogo, banner, and discart for a movie.
func (c *Client) GetMovieArtworks(ctx context.Context, tmdbID int64) (*MovieArtworks, error) {
	if tmdbID <= 0 {
		return nil, errors.New("invalid tmdbID")
	}

	reqURL := fmt.Sprintf("%s/movies/%d", c.baseURL, tmdbID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("api-key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return &MovieArtworks{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	var d struct {
		HDMovieLogo []ArtworkImage `json:"hdmovielogo"`
		MovieBanner []ArtworkImage `json:"moviebanner"`
		MovieDisc   []ArtworkImage `json:"moviedisc"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: failed to parse fanart response: %v", ports.ErrBadResponse, err)
	}

	art := &MovieArtworks{}
	// Prefer zh/cn, else first logo
	for _, img := range d.HDMovieLogo {
		if strings.HasPrefix(img.Lang, "zh") || strings.HasPrefix(img.Lang, "cn") || art.ClearLogo == "" {
			art.ClearLogo = img.URL
			if strings.HasPrefix(img.Lang, "zh") {
				break
			}
		}
	}
	if len(d.MovieBanner) > 0 {
		art.Banner = d.MovieBanner[0].URL
	}
	if len(d.MovieDisc) > 0 {
		art.DiscArt = d.MovieDisc[0].URL
	}

	return art, nil
}
