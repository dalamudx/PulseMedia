package tmdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func diagnosticRateLimited(err error) bool { _, ok := ports.IsRateLimited(err); return ok }

const diagnosticPoster = "https://image.tmdb.org/t/p/w500/pB8BM7pdSp6B6Ih7QZ4DrQ3PmJK.jpg"

// ProbeDiagnostic performs exactly one fixed read without populating metadata caches.
func (c *Client) ProbeDiagnostic(parent context.Context, target ports.MetadataDiagnosticTarget) ports.MetadataDiagnosticResult {
	result := ports.MetadataDiagnosticResult{Target: target, Category: "unavailable"}
	if target != ports.DiagnosticTMDBAPI && target != ports.DiagnosticTMDBImage {
		return result
	}
	if c.httpClient == nil || c.budget == nil {
		return result
	}
	client := *c.httpClient
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if target == ports.DiagnosticTMDBImage {
		if client.Timeout <= 0 || client.Timeout > 15*time.Second {
			client.Timeout = 15 * time.Second
		}
	} else if client.Timeout <= 0 {
		client.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, client.Timeout)
	defer cancel()
	fail := func(err error) ports.MetadataDiagnosticResult {
		result.Category = "unavailable"
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ports.ErrTimeout):
			result.Category = "timeout"
		case errors.Is(parent.Err(), context.Canceled), errors.Is(err, context.Canceled):
			result.Category = "cancelled"
		case diagnosticRateLimited(err):
			result.Category = "rate_limited"
		}
		return result
	}
	release, err := c.budget.Acquire(ctx)
	if err != nil {
		return fail(err)
	}
	if release == nil {
		return result
	}
	defer release()
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	endpoint := c.baseURL + "/movie/550?language=en-US"
	if target == ports.DiagnosticTMDBImage {
		endpoint = diagnosticPoster
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result
	}
	if target == ports.DiagnosticTMDBAPI {
		req.Header.Set("Accept", "application/json")
		if c.apiKey == "" {
			result.Category = "not_configured"
			return result
		}
		if strings.HasPrefix(c.apiKey, "ey") || len(c.apiKey) > 50 {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		} else {
			q := req.URL.Query()
			q.Set("api_key", c.apiKey)
			req.URL.RawQuery = q.Encode()
		}
	} else {
		req.Header.Set("Accept", "image/jpeg, image/png")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fail(err)
	}
	result.HostResponded = true
	// Keep admission until the physical response body has closed.
	category := func() string {
		switch {
		case resp.StatusCode == 429:
			seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64)
			if err != nil || seconds <= 0 {
				seconds = 5
			}
			seconds = min(seconds, 300)
			c.budget.RecordRateLimit(time.Duration(seconds) * time.Second)
			return "rate_limited"
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			return "auth_required"
		case resp.StatusCode != 200:
			return "host_response"
		}
		limit := int64(5 << 20)
		if target == ports.DiagnosticTMDBImage {
			limit = 8 << 20
		}
		if resp.ContentLength > limit {
			return "bad_response"
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil || ctx.Err() != nil {
			return fail(err).Category
		}
		if int64(len(body)) > limit {
			return "bad_response"
		}
		if target == ports.DiagnosticTMDBAPI {
			var value struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			}
			if json.Unmarshal(body, &value) != nil || value.ID != 550 || strings.TrimSpace(value.Title) == "" {
				return "bad_response"
			}
			return "available"
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
			return "bad_response"
		}
		if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 || config.Width > (4<<20)/config.Height {
			return "bad_response"
		}
		if _, err = decode(bytes.NewReader(body)); err != nil {
			return "bad_response"
		}
		result.ImageRetrieved = true
		return "available"
	}()
	closeErr := resp.Body.Close()
	result.Category = category
	if ctx.Err() != nil || closeErr != nil {
		result.ImageRetrieved = false
		return fail(closeErr)
	}
	return result
}
