package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/notify/transport"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.Notifier = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	botToken   string
	chatID     string
}

func NewClient(botToken, chatID string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	// Never forward credentials/signatures or report redirected requests as delivery.
	clone := *httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpClient = &clone
	return &Client{
		httpClient: httpClient,
		botToken:   botToken,
		chatID:     chatID,
	}
}

func (c *Client) Send(ctx context.Context, n ports.Notification) error {
	msgText := fmt.Sprintf("%s\n%s\n\n事件 ID: %s", n.Title, n.Body, n.EventID)

	payloadBytes, err := json.Marshal(map[string]any{
		"chat_id": c.chatID,
		"text":    msgText,
	})
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", c.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return ports.ErrBadResponse
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return transport.Error(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ports.ErrAuthRequired
	}
	if resp.StatusCode >= 500 {
		return ports.ErrUnavailable
	}
	const maxResponse = 64 * 1024
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	var result struct {
		OK         bool `json:"ok"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := json.Unmarshal(body, &result)
	if resp.StatusCode == http.StatusTooManyRequests {
		retry := 10 * time.Second
		if readErr == nil && len(body) <= maxResponse && decodeErr == nil && result.Parameters.RetryAfter > 0 && result.Parameters.RetryAfter <= 86400 {
			retry = time.Duration(result.Parameters.RetryAfter) * time.Second
		}
		return ports.NewRateLimitError(retry, "Telegram bot rate limit reached")
	}
	if readErr != nil {
		return transport.Error(ctx, readErr)
	}
	if resp.StatusCode >= 500 {
		return ports.ErrUnavailable
	}
	if resp.StatusCode == http.StatusOK {
		if len(body) > maxResponse || decodeErr != nil || !result.OK {
			return ports.ErrBadResponse
		}
		return nil
	}

	return fmt.Errorf("%w: telegram status %d", ports.ErrBadResponse, resp.StatusCode)
}

func (c *Client) Test(ctx context.Context) error {
	return c.Send(ctx, ports.Notification{
		EventID:   "test",
		Title:     "PulseMedia 测试通知",
		Body:      "用于检查通知通道连通性",
		Timestamp: time.Now(),
	})
}
