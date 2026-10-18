package qbittorrent

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strconv"
	"time"
)

const torrentResponseLimit int64 = 8 << 20

// httpFailure intentionally drops untrusted transport error text and URLs.
func httpFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}
func httpStatusFailure(r *http.Response) error {
	switch r.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ports.ErrAuthRequired
	case http.StatusNotFound:
		return ports.ErrNotFound
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return ports.ErrTimeout
	case http.StatusTooManyRequests:
		var retry time.Duration
		if seconds, e := strconv.ParseInt(r.Header.Get("Retry-After"), 10, 64); e == nil && seconds > 0 && seconds <= 86400 {
			retry = time.Duration(seconds) * time.Second
		}
		return ports.NewRateLimitError(retry, "downloader request rate limited")
	}
	if r.StatusCode >= 500 {
		return ports.ErrUnavailable
	}
	return ports.ErrBadResponse
}
func (c *Client) apiHTTPFailure(r *http.Response) error {
	err := httpStatusFailure(r)
	if errors.Is(err, ports.ErrAuthRequired) {
		c.mu.Lock()
		c.loggedIn = false
		c.mu.Unlock()
	}
	return err
}
func readHTTPBody(ctx context.Context, body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, httpFailure(ctx, err)
	}
	if int64(len(data)) > limit {
		return nil, ports.ErrBadResponse
	}
	return data, nil
}
