package bangumi

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
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type AnimeItem = ports.BangumiItem
type DaySchedule = ports.BangumiDay

const maxBodyBytes = 2 << 20

type Client struct {
	httpClient *http.Client
	endpoint   string
}

func NewClient(baseURL string, supplied *http.Client) *Client {
	if baseURL == "" {
		baseURL = "https://api.bgm.tv"
	}
	c := &Client{}
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return c
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/calendar"
	u.RawPath = ""
	c.endpoint = u.String()
	var client http.Client
	if supplied != nil {
		client = *supplied
	}
	if client.Timeout <= 0 || client.Timeout > 10*time.Second {
		client.Timeout = 10 * time.Second
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.httpClient = &client
	return c
}
func (c *Client) GetCalendar(ctx context.Context) ([]DaySchedule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.httpClient == nil || c.endpoint == "" {
		return nil, ports.ErrRejected
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, ports.ErrRejected
	}
	req.Header.Set("User-Agent", "PulseMedia/0.1.0 (https://github.com/dalamudx/PulseMedia)")
	req.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, readError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ports.ErrAuthRequired
	case http.StatusTooManyRequests:
		seconds, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		if seconds < 0 || seconds > 86400 {
			seconds = 0
		}
		return nil, ports.NewRateLimitError(time.Duration(seconds)*time.Second, "Bangumi request limited")
	default:
		if response.StatusCode >= 500 {
			return nil, ports.ErrUnavailable
		}
		return nil, ports.ErrBadResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return nil, readError(ctx, err)
	}
	if len(data) > maxBodyBytes || !utf8.Valid(data) {
		return nil, ports.ErrBadResponse
	}
	return decodeCalendar(data)
}
func readError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}
