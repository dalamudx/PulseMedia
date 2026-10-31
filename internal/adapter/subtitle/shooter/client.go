package shooter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	defaultAPIURL = "https://www.shooter.cn/api/subapi.php"
	maxAPIBytes   = 128 * 1024
	maxPackages   = 64
	maxFiles      = 256
)

var _ ports.ChineseSubtitleProvider = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	apiURL     *url.URL
}

func NewClient(apiURL string, httpClient *http.Client) *Client {
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	cloned := http.Client{Timeout: 10 * time.Second}
	if httpClient != nil {
		cloned = *httpClient
	}
	if cloned.Timeout <= 0 {
		cloned.Timeout = 10 * time.Second
	}
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cloned.Jar = nil
	base, err := url.Parse(apiURL)
	if err != nil || strings.Contains(apiURL, "#") || !safeURL(base) {
		base = nil
	}
	return &Client{httpClient: &cloned, apiURL: base}
}

func safeURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// FindChineseSubtitle uses the historic Shooter form/direct-link protocol. Only
// the configured API origin is trusted; this is not a list of current CDN hosts.
func (c *Client) FindChineseSubtitle(ctx context.Context, hash ports.SubtitleVideoHash) ([]byte, error) {
	if !media.ValidShooterHash(hash.Hash, hash.SizeBytes) || c.apiURL == nil {
		return nil, ports.ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return nil, networkError(ctx, err)
	}
	form := url.Values{"filehash": {hash.Hash}, "pathinfo": {""}, "format": {"json"}, "lang": {"chn"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, ports.ErrRejected
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.readResponse(ctx, req, maxAPIBytes)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(body, []byte{0xff}) {
		return nil, nil
	}
	link, err := c.subtitleLink(body)
	if err != nil || link == "" {
		return nil, err
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	body, err = c.readResponse(ctx, req, ports.MaxSubtitleBytes)
	if err != nil {
		return nil, err
	}
	normalized, err := media.NormalizeChineseSRT(body)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	return normalized, nil
}

func (c *Client) subtitleLink(body []byte) (string, error) {
	links, err := c.subtitleLinks(body)
	if err != nil || len(links) == 0 {
		return "", err
	}
	return links[0], nil
}

func (c *Client) subtitleLinks(body []byte) ([]string, error) {
	var packages []*struct{ Files json.RawMessage }
	if err := json.Unmarshal(body, &packages); err != nil || packages == nil || len(packages) > maxPackages {
		return nil, ports.ErrBadResponse
	}
	selected, count := []string{}, 0
	for _, pkg := range packages {
		if pkg == nil {
			return nil, ports.ErrBadResponse
		}
		var files []*struct {
			Ext  *string
			Link *string
		}
		if err := json.Unmarshal(pkg.Files, &files); err != nil || files == nil {
			return nil, ports.ErrBadResponse
		}
		count += len(files)
		if len(files) > 64 || count > maxFiles {
			return nil, ports.ErrBadResponse
		}
		for _, f := range files {
			if f == nil || f.Ext == nil || f.Link == nil || *f.Ext == "" || *f.Link == "" {
				return nil, ports.ErrBadResponse
			}
			u, err := url.Parse(*f.Link)
			if err != nil || strings.Contains(*f.Link, "#") || !safeURL(u) || !sameOrigin(c.apiURL, u) {
				return nil, ports.ErrBadResponse
			}
			ext := strings.TrimPrefix(strings.ToLower(*f.Ext), ".")
			if ext == "srt" {
				selected = append(selected, u.String())
			}
		}
	}
	return selected, nil
}

func (c *Client) readResponse(ctx context.Context, req *http.Request, limit int64) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, networkError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		var retry time.Duration
		// Only bounded integer retry advice is needed; never echo an upstream header.
		if seconds, e := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 32); e == nil && seconds > 0 {
			retry = time.Duration(seconds) * time.Second
		}
		return nil, ports.NewRateLimitError(retry, "shooter")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ports.ErrBadResponse
	}
	if resp.ContentLength > limit {
		return nil, ports.ErrBadResponse
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, networkError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, networkError(ctx, err)
	}
	if int64(len(body)) > limit {
		return nil, ports.ErrBadResponse
	}
	return body, nil
}

// Return only native error contracts: URL errors can contain signed links.
func networkError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var timeout net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}
