package qbittorrent

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"strconv"
	"strings"
)

// qBittorrent 5.2 returns 204 when login is unnecessary for the configured
// trusted subnet. Require a protected API proof before accepting that reply.
// The caller holds mu, so error mapping must not reacquire the authentication lock.
func (c *Client) confirmNoContentLogin(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/app/webapiVersion", nil)
	if err != nil {
		return ports.ErrBadResponse
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return httpFailure(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return httpStatusFailure(response)
	}
	raw, err := readHTTPBody(ctx, response.Body, 64)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), ".")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "2" {
		return ports.ErrBadResponse
	}
	for _, part := range parts {
		if part == "" {
			return ports.ErrBadResponse
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return ports.ErrBadResponse
			}
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return ports.ErrBadResponse
		}
	}
	return nil
}
