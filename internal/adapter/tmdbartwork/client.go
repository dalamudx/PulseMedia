// Package tmdbartwork fetches bounded posters from the trusted TMDB image origin.
package tmdbartwork

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	requestTimeout = 15 * time.Second
	maxBodyBytes   = 8 << 20
	maxDimension   = 4096
	maxPixels      = 4 << 20
)

// Client shares the application-owned metadata budget.
type Client struct {
	httpClient *http.Client
	budget     ports.MetadataRequestBudget
}

var _ ports.MetadataArtworkFetcher = (*Client)(nil)

// New clones client configuration and disables redirects and cookie credentials.
// A missing budget fails closed; the caller owns runtime budget assembly.
func New(client *http.Client, budget ports.MetadataRequestBudget) *Client {
	cloned := http.Client{}
	if client != nil {
		cloned = *client
	}
	if cloned.Timeout <= 0 || cloned.Timeout > requestTimeout {
		cloned.Timeout = requestTimeout
	}
	cloned.Jar = nil
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &cloned, budget: budget}
}

// FetchPoster holds physical admission through body close, decode and encoding.
// Synchronous work must actually return before a canceled call releases its slot.
func (c *Client) FetchPoster(ctx context.Context, path string) ([]byte, error) {
	target := media.TMDBPosterURL(path)
	if target == "" || c.budget == nil || ctx.Err() != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, c.httpClient.Timeout)
	defer cancel()
	release, err := c.budget.Acquire(ctx)
	if err != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	if release == nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	defer release()
	if ctx.Err() != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	data, err := c.fetch(ctx, target)
	// Check after body Close too: an uncooperative Close may outlive cancellation.
	if err != nil || ctx.Err() != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	return data, nil
}

func (c *Client) fetch(ctx context.Context, target string) (data []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			data, err = nil, ports.ErrMetadataArtworkUnavailable
		}
	}()
	if resp.StatusCode == http.StatusTooManyRequests {
		c.budget.RecordRateLimit(retryAfter(resp.Header.Get("Retry-After")))
	}
	if ctx.Err() != nil || resp.StatusCode != http.StatusOK || resp.ContentLength > maxBodyBytes {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes || ctx.Err() != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	var config image.Config
	var decode func(io.Reader) (image.Image, error)
	switch {
	case bytes.HasPrefix(body, []byte{0xff, 0xd8}):
		config, err = jpeg.DecodeConfig(bytes.NewReader(body))
		decode = jpeg.Decode
	case bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")):
		config, err = png.DecodeConfig(bytes.NewReader(body))
		decode = png.Decode
	default:
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > maxDimension || config.Height > maxDimension || config.Width > maxPixels/config.Height || ctx.Err() != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	img, err := decode(bytes.NewReader(body))
	if err != nil || ctx.Err() != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	out := posterWriter{ctx: ctx, data: make([]byte, 0, maxBodyBytes)}
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil || ctx.Err() != nil {
		return nil, ports.ErrMetadataArtworkUnavailable
	}
	return out.data, nil
}

// posterWriter bounds allocation as well as output and checks cancellation while encoding.
type posterWriter struct {
	ctx  context.Context
	data []byte
}

func (w *posterWriter) Write(p []byte) (int, error) {
	if w.ctx.Err() != nil || len(p) > maxBodyBytes-len(w.data) {
		return 0, ports.ErrMetadataArtworkUnavailable
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func retryAfter(raw string) time.Duration {
	// Numeric seconds only; no wall clock dependency and no untrusted error text.
	seconds, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return time.Second
	}
	if seconds > 300 {
		seconds = 300
	}
	if seconds == 0 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second
}
