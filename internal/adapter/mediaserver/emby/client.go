package emby

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.MediaServerClient = (*Client)(nil)

const maxBody = 8 << 20

type Client struct {
	httpClient      *http.Client
	baseURL, apiKey string
	valid           bool
}

func NewClient(baseURL, apiKey string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	if clone.Timeout <= 0 || clone.Timeout > 10*time.Second {
		clone.Timeout = 10 * time.Second
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u, e := url.Parse(baseURL)
	valid := e == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawPath == "" && strings.TrimSpace(apiKey) != "" && !strings.ContainsAny(apiKey, "\r\n")
	if valid {
		valid = u.Path == "" || u.Path == "/" || path.Clean(u.Path) == strings.TrimRight(u.Path, "/")
	}
	return &Client{&clone, strings.TrimRight(baseURL, "/"), apiKey, valid}
}
func (c *Client) request(ctx context.Context, method, endpoint string, payload []byte) (*http.Response, error) {
	if !c.valid {
		return nil, ports.ErrBadResponse
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, bytes.NewReader(payload))
	if e != nil {
		return nil, ports.ErrBadResponse
	}
	req.Header.Set("X-Emby-Token", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := c.httpClient.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var n net.Error
		if errors.As(e, &n) && n.Timeout() {
			return nil, ports.ErrTimeout
		}
		return nil, ports.ErrUnavailable
	}
	return resp, nil
}
func status(resp *http.Response, accepted ...int) error {
	for _, code := range accepted {
		if resp.StatusCode == code {
			return nil
		}
	}
	switch resp.StatusCode {
	case 401, 403:
		return ports.ErrAuthRequired
	case 429:
		var wait time.Duration
		if n, e := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); e == nil && n > 0 && n <= 86400 {
			wait = time.Duration(n) * time.Second
		} else if at, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
			wait = time.Until(at)
			if wait < 0 || wait > 24*time.Hour {
				wait = 0
			}
		}
		return ports.NewRateLimitError(wait, "Emby HTTP 429")
	}
	if resp.StatusCode >= 500 {
		return ports.ErrUnavailable
	}
	return ports.ErrBadResponse
}
func responseJSON(resp *http.Response, target any) error {
	b, e := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if e != nil || len(b) > maxBody || !validJSON(b) || json.Unmarshal(b, target) != nil {
		return ports.ErrBadResponse
	}
	return nil
}
func (c *Client) Test(ctx context.Context) error {
	resp, e := c.request(ctx, http.MethodGet, "/System/Info", nil)
	if e != nil {
		return e
	}
	defer func() { _ = resp.Body.Close() }()
	if e = status(resp, 200); e != nil {
		return e
	}
	var info struct {
		ServerName string
		Version    string
	}
	if responseJSON(resp, &info) != nil || strings.TrimSpace(info.ServerName) == "" || strings.TrimSpace(info.Version) == "" {
		return ports.ErrBadResponse
	}
	return nil
}

// Refresh only explicit mapped paths. Never fall back to a server-wide scan.
func (c *Client) RefreshPaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if len(paths) > 128 {
		return ports.ErrBadResponse
	}
	type update struct {
		Path       string
		UpdateType string
	}
	updates := make([]update, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		if !validPath(p) {
			return ports.ErrBadResponse
		}
		if !seen[p] {
			seen[p] = true
			updates = append(updates, update{p, "Created"})
		}
	}
	payload, _ := json.Marshal(struct{ Updates []update }{updates})
	resp, e := c.request(ctx, http.MethodPost, "/Library/Media/Updated", payload)
	if e != nil {
		return e
	}
	defer func() { _ = resp.Body.Close() }()
	return status(resp, 200, 204)
}
func (c *Client) FindByPath(ctx context.Context, itemPath string) (string, bool, error) {
	if !validPath(itemPath) {
		return "", false, ports.ErrBadResponse
	}
	// Emby has no documented Path filter: prove uniqueness against a complete inventory.
	resp, e := c.request(ctx, http.MethodGet, "/Items?Recursive=true&MediaTypes=Video&Fields=Path&EnableTotalRecordCount=true&EnableImages=false&EnableUserData=false", nil)
	if e != nil {
		return "", false, e
	}
	defer func() { _ = resp.Body.Close() }()
	if e = status(resp, 200); e != nil {
		return "", false, e
	}
	var proof struct {
		Items *[]struct {
			ID   string `json:"Id"`
			Path string
		}
		Total *int64 `json:"TotalRecordCount"`
	}
	if responseJSON(resp, &proof) != nil || proof.Items == nil || proof.Total == nil || *proof.Total != int64(len(*proof.Items)) {
		return "", false, ports.ErrBadResponse
	}
	seen := map[string]bool{}
	match := ""
	for _, item := range *proof.Items {
		if strings.TrimSpace(item.ID) == "" || len(item.ID) > 256 || strings.IndexFunc(item.ID, unicode.IsControl) >= 0 || !validPath(item.Path) || seen[item.ID] {
			return "", false, ports.ErrBadResponse
		}
		seen[item.ID] = true
		if item.Path == itemPath {
			if match != "" {
				return "", false, ports.ErrBadResponse
			}
			match = item.ID
		}
	}
	return match, match != "", nil
}
func validPath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return false
	}
	normalized := strings.ReplaceAll(p, "\\", "/")
	absolute := strings.HasPrefix(normalized, "/") || len(normalized) > 3 && ((normalized[0] >= 'A' && normalized[0] <= 'Z') || (normalized[0] >= 'a' && normalized[0] <= 'z')) && normalized[1:3] == ":/"
	if !absolute {
		return false
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return true
}
