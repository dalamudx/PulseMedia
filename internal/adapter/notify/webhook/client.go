package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/notify/transport"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.Notifier = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	url        string
	secret     string
}

func NewClient(url, secret string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	// Never forward credentials/signatures or report redirected requests as delivery.
	clone := *httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpClient = &clone
	return &Client{
		httpClient: httpClient,
		url:        url,
		secret:     secret,
	}
}

func (c *Client) Send(ctx context.Context, n ports.Notification) error {
	payloadBytes, err := json.Marshal(n)
	if err != nil {
		return ports.ErrBadResponse
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payloadBytes))
	if err != nil {
		return ports.ErrBadResponse
	}

	req.Header.Set("Content-Type", "application/json")
	// Deduplication header for receiver idempotency
	req.Header.Set("X-PulseMedia-Event-ID", n.EventID)

	// HMAC-SHA256 signature if secret is configured
	if c.secret != "" {
		mac := hmac.New(sha256.New, []byte(c.secret))
		mac.Write(payloadBytes)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-PulseMedia-Signature", "sha256="+sig)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return transport.Error(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		retry := time.Minute
		if seconds, parseErr := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 32); parseErr == nil && seconds > 0 && seconds <= 86400 {
			retry = time.Duration(seconds) * time.Second
		} else if at, parseErr := http.ParseTime(resp.Header.Get("Retry-After")); parseErr == nil {
			wait := time.Until(at)
			if wait > 0 && wait <= 24*time.Hour {
				retry = wait
			}
		}
		return &ports.RateLimitError{RetryAfter: retry, Reason: "webhook rate limited"}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ports.ErrAuthRequired
	}
	if resp.StatusCode >= 500 {
		return ports.ErrUnavailable
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	return fmt.Errorf("%w: webhook returned status %d", ports.ErrBadResponse, resp.StatusCode)
}

func (c *Client) Test(ctx context.Context) error {
	return c.Send(ctx, ports.Notification{
		EventID:   "test-event-id",
		Topic:     "system.test",
		Title:     "PulseMedia Webhook 测试通知",
		Body:      "用于检查通知通道连通性",
		Severity:  "info",
		Timestamp: time.Now().UTC(),
	})
}
