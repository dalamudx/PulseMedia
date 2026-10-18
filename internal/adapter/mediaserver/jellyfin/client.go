package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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

// RefreshPaths notifies Jellyfin about new or updated media files.
func (c *Client) RefreshPaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	type pathUpdate struct {
		Path       string `json:"Path"`
		UpdateType string `json:"UpdateType"`
	}

	updates := make([]pathUpdate, len(paths))
	for i, p := range paths {
		updates[i] = pathUpdate{
			Path:       p,
			UpdateType: "Created",
		}
	}

	payloadBytes, err := json.Marshal(map[string]any{
		"Updates": updates,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal refresh payload: %w", err)
	}

	reqURL := fmt.Sprintf("%s/Library/Media/Updated", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return ports.ErrBadResponse
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "MediaBrowser Token="+strconv.Quote(c.apiKey))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return transportFailure(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	return responseFailure(resp, http.StatusOK, http.StatusNoContent)

}

// FindByPath checks whether Jellyfin has indexed an item at the specified path.
func (c *Client) FindByPath(ctx context.Context, itemPath string) (string, bool, error) {
	// Jellyfin has no Path query filter. Request a complete recursive video
	// inventory and prove a unique exact path locally; never confirm a partial page.
	reqURL := c.baseURL + "/Items?Recursive=true&MediaTypes=Video&Fields=Path&EnableTotalRecordCount=true&EnableImages=false&EnableUserData=false"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", false, ports.ErrBadResponse
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "MediaBrowser Token="+strconv.Quote(c.apiKey))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", false, transportFailure(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if err := responseFailure(resp, http.StatusOK); err != nil {
		return "", false, err
	}

	return findItem(resp, itemPath)
}

// Test verifies connection and authentication with Jellyfin.
func (c *Client) Test(ctx context.Context) error {
	reqURL := fmt.Sprintf("%s/System/Info", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return ports.ErrBadResponse
	}

	req.Header.Set("Authorization", "MediaBrowser Token="+strconv.Quote(c.apiKey))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return transportFailure(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	return responseFailure(resp, http.StatusOK)

}
