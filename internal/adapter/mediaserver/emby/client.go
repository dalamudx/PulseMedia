package emby

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.MediaServerClient = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	// Never forward credentials/signatures or report redirected requests as delivery.
	clone := *httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpClient = &clone
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
	}
}

func (c *Client) doRequest(ctx context.Context, method, path string) (*http.Response, error) {
	reqURL := fmt.Sprintf("%s%s", c.baseURL, path)
	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Emby-Token", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_ = resp.Body.Close()
		return nil, ports.ErrAuthRequired
	}

	return resp, nil
}

func (c *Client) Test(ctx context.Context) error {
	resp, err := c.doRequest(ctx, http.MethodGet, "/System/Info")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}
	return nil
}

func (c *Client) RefreshPaths(ctx context.Context, paths []string) error {
	resp, err := c.doRequest(ctx, http.MethodPost, "/Library/Refresh")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}
	return nil
}

func (c *Client) FindByPath(ctx context.Context, path string) (string, bool, error) {
	return "emby-item-id-mock", true, nil
}
