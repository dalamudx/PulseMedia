package fanart

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const apiOrigin = "https://webservice.fanart.tv/v3"
const imageOrigin = "https://assets.fanart.tv"
const maxJSONBytes = 2 << 20
const maxImageBytes = 8 << 20

type Client struct {
	httpClient *http.Client
	apiKey     string
}

var _ ports.MediaArtworkProvider = (*Client)(nil)

func NewClient(apiKey string, supplied *http.Client) *Client {
	var hc http.Client
	if supplied != nil {
		hc = *supplied
	}
	if hc.Timeout <= 0 || hc.Timeout > 10*time.Second {
		hc.Timeout = 10 * time.Second
	}
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &hc, apiKey: apiKey}
}

func identityPath(id ports.ArtworkIdentity) (string, bool) {
	if id.ID <= 0 {
		return "", false
	}
	switch {
	case id.MediaType == "movie" && id.Provider == "tmdb":
		return "movies/" + strconv.FormatInt(id.ID, 10), true
	case id.MediaType == "tv" && id.Provider == "tvdb":
		return "tv/" + strconv.FormatInt(id.ID, 10), true
	default:
		return "", false
	}
}

func (c *Client) GetArtworks(ctx context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) {
	var zero ports.MediaArtworks
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	path, ok := identityPath(id)
	if !ok || c == nil || c.httpClient == nil || strings.TrimSpace(c.apiKey) == "" || len(c.apiKey) > 512 || strings.ContainsAny(c.apiKey, "\r\n") {
		return zero, ports.ErrRejected
	}
	// Fanart's documented API key is a query parameter. Never return transport URLs/errors.
	data, err := c.get(ctx, apiOrigin+"/"+path+"?"+url.Values{"api_key": {c.apiKey}}.Encode(), maxJSONBytes)
	if err != nil {
		return zero, err
	}
	return decodeArtworks(data, id)
}

func (c *Client) get(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ports.ErrRejected
	}
	req.Header.Set("User-Agent", "PulseMedia/0.1.0 (https://github.com/dalamudx/PulseMedia)")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, readError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ports.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ports.ErrAuthRequired
	case http.StatusTooManyRequests:
		return nil, ports.NewRateLimitError(retryAfter(response.Header), "Fanart request limited")
	default:
		if response.StatusCode >= 500 {
			return nil, ports.ErrUnavailable
		}
		return nil, ports.ErrBadResponse
	}
	if response.ContentLength > limit {
		return nil, ports.ErrBadResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, readError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ports.ErrBadResponse
	}
	return data, nil
}

func retryAfter(h http.Header) time.Duration {
	raw := h.Get("Retry-After")
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds >= 0 && seconds <= 86400 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	// A server Date avoids inventing a local clock when interpreting HTTP-date advice.
	until, e1 := http.ParseTime(raw)
	now, e2 := http.ParseTime(h.Get("Date"))
	if e1 != nil || e2 != nil || until.Before(now) || until.Sub(now) > 24*time.Hour {
		return 0
	}
	return until.Sub(now)
}

func readError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	var timeout net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}

// MovieArtworks preserves the original prototype's read-only return shape.
// Production callers use opaque references through MediaArtworkProvider.
type MovieArtworks struct {
	ClearLogo string `json:"clear_logo"`
	Banner    string `json:"banner"`
	DiscArt   string `json:"disc_art"`
}

func (c *Client) GetMovieArtworks(ctx context.Context, tmdbID int64) (*MovieArtworks, error) {
	art, err := c.GetArtworks(ctx, ports.ArtworkIdentity{MediaType: "movie", Provider: "tmdb", ID: tmdbID})
	if errors.Is(err, ports.ErrNotFound) {
		return &MovieArtworks{}, nil
	}
	if err != nil {
		return nil, err
	}
	full := func(p string) string {
		if p == "" {
			return ""
		}
		return imageOrigin + p
	}
	return &MovieArtworks{ClearLogo: full(art.ClearLogo), Banner: full(art.Banner), DiscArt: full(art.DiscArt)}, nil
}
