package siteattendance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const maxBodyBytes = 1 << 20

type Client struct{ httpClient *http.Client }

// NewClient copies the injected client so redirect policy never mutates its owner.
func NewClient(client *http.Client) *Client {
	if client == nil {
		return &Client{}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout <= 0 {
		copy.Timeout = 10 * time.Second
	}
	return &Client{httpClient: &copy}
}

var _ ports.SiteAttendanceClient = (*Client)(nil)

func (c *Client) CheckinSite(ctx context.Context, siteURL, cookie string) (*ports.SiteCheckinResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.httpClient == nil {
		return nil, ports.ErrUnavailable
	}
	checkinURL := strings.TrimRight(siteURL, "/")
	if !strings.HasSuffix(checkinURL, "attendance.php") {
		checkinURL += "/attendance.php"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkinURL, nil)
	if err != nil || req.URL.Host == "" || req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return nil, ports.ErrRejected
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) PulseMedia/0.1.0")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &ports.SiteCheckinResult{Message: "Cookie已失效，需重新登录"}, ports.ErrAuthRequired
	}
	if resp.StatusCode != http.StatusOK {
		return &ports.SiteCheckinResult{Message: fmt.Sprintf("站点返回状态码 %d", resp.StatusCode)}, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(body) > maxBodyBytes {
		return nil, ports.ErrBadResponse
	}
	for _, marker := range []string{"这是您的第", "签到成功", "已签到"} {
		if strings.Contains(string(body), marker) {
			return &ports.SiteCheckinResult{Success: true, Message: "签到成功，已记录连续签到天数"}, nil
		}
	}
	return &ports.SiteCheckinResult{Message: "站点未确认签到结果"}, nil
}
