package plex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.MediaServerClient = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

func NewClient(baseURL, token string, httpClient *http.Client) *Client {
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
		token:      token,
	}
}

type sectionInfo struct {
	Key       string   `json:"key"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Locations []string `json:"locations"`
}

// RefreshPaths notifies Plex Media Server to scan library folders matching the specified paths.
func (c *Client) RefreshPaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	sections, err := c.getSections(ctx)
	if err != nil {
		return fmt.Errorf("failed to get plex library sections: %w", err)
	}

	for _, p := range paths {
		targetFolder := filepath.Dir(p)
		matchedSection := ""

		for _, sec := range sections {
			for _, loc := range sec.Locations {
				if strings.HasPrefix(p, loc) || strings.HasPrefix(targetFolder, loc) {
					matchedSection = sec.Key
					break
				}
			}
			if matchedSection != "" {
				break
			}
		}

		if matchedSection != "" {
			// Partial folder scan: GET /library/sections/{key}/refresh?path={folder}
			refreshURL := fmt.Sprintf("%s/library/sections/%s/refresh?path=%s",
				c.baseURL, matchedSection, url.QueryEscape(targetFolder))
			if err := c.doGet(ctx, refreshURL, nil); err != nil {
				return fmt.Errorf("failed to refresh section %s for path %s: %w", matchedSection, p, err)
			}
		} else {
			// General refresh on all sections
			for _, sec := range sections {
				refreshURL := fmt.Sprintf("%s/library/sections/%s/refresh", c.baseURL, sec.Key)
				_ = c.doGet(ctx, refreshURL, nil)
			}
		}
	}

	return nil
}

// FindByPath checks whether Plex has indexed an item with a media part matching the file path.
func (c *Client) FindByPath(ctx context.Context, itemPath string) (string, bool, error) {
	sections, err := c.getSections(ctx)
	if err != nil {
		return "", false, fmt.Errorf("failed to get plex sections: %w", err)
	}

	for _, sec := range sections {
		secItemsURL := fmt.Sprintf("%s/library/sections/%s/all", c.baseURL, sec.Key)
		var resp struct {
			MediaContainer struct {
				Metadata []struct {
					RatingKey string `json:"ratingKey"`
					Title     string `json:"title"`
					Media     []struct {
						Part []struct {
							File string `json:"file"`
						} `json:"Part"`
					} `json:"Media"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}

		err := c.doGet(ctx, secItemsURL, &resp)
		if err != nil {
			continue
		}

		for _, meta := range resp.MediaContainer.Metadata {
			for _, med := range meta.Media {
				for _, part := range med.Part {
					if part.File == itemPath {
						return meta.RatingKey, true, nil
					}
				}
			}
		}
	}

	return "", false, nil
}

// Test verifies connection and authentication with Plex.
func (c *Client) Test(ctx context.Context) error {
	reqURL := fmt.Sprintf("%s/identity", c.baseURL)
	return c.doGet(ctx, reqURL, nil)
}

func (c *Client) getSections(ctx context.Context) ([]sectionInfo, error) {
	reqURL := fmt.Sprintf("%s/library/sections", c.baseURL)
	var resp struct {
		MediaContainer struct {
			Directory []struct {
				Key      string `json:"key"`
				Type     string `json:"type"`
				Title    string `json:"title"`
				Location []struct {
					Path string `json:"path"`
				} `json:"Location"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}

	if err := c.doGet(ctx, reqURL, &resp); err != nil {
		return nil, err
	}

	sections := make([]sectionInfo, 0, len(resp.MediaContainer.Directory))
	for _, d := range resp.MediaContainer.Directory {
		locs := make([]string, 0, len(d.Location))
		for _, l := range d.Location {
			locs = append(locs, l.Path)
		}
		sections = append(sections, sectionInfo{
			Key:       d.Key,
			Type:      d.Type,
			Title:     d.Title,
			Locations: locs,
		})
	}

	return sections, nil
}

func (c *Client) doGet(ctx context.Context, rawURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)
	req.Header.Set("X-Plex-Client-Identifier", "PulseMedia")
	req.Header.Set("X-Plex-Product", "PulseMedia")
	req.Header.Set("X-Plex-Version", "0.1.0-beta")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ports.ErrTimeout
		}
		return fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ports.ErrAuthRequired
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: Plex server error (status %d)", ports.ErrUnavailable, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	if target != nil {
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			return fmt.Errorf("%w: failed to parse response JSON: %v", ports.ErrBadResponse, err)
		}
	}

	return nil
}
