package im

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type Client struct {
	httpClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{httpClient: httpClient}
}

// SendNotification sends a formatted notification to WeChat, DingTalk, Feishu, Bark.
func (c *Client) SendNotification(ctx context.Context, channel, webhookURL, title, text string) error {
	if webhookURL == "" {
		return errors.New("webhook URL cannot be empty")
	}

	var reqBody []byte
	var err error

	switch channel {
	case "wechat": // 企业微信机器人
		reqBody, err = json.Marshal(map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"content": fmt.Sprintf("### %s\n%s", title, text),
			},
		})
	case "dingtalk": // 钉钉机器人
		reqBody, err = json.Marshal(map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": title,
				"text":  fmt.Sprintf("### %s\n\n%s", title, text),
			},
		})
	case "feishu": // 飞书机器人
		reqBody, err = json.Marshal(map[string]any{
			"msg_type": "text",
			"content": map[string]string{
				"text": fmt.Sprintf("%s\n\n%s", title, text),
			},
		})
	case "bark": // Bark iOS
		reqBody, err = json.Marshal(map[string]string{
			"title": title,
			"body":  text,
			"group": "PulseMedia",
		})
	default: // 通用 JSON Webhook
		reqBody, err = json.Marshal(map[string]string{
			"title":   title,
			"content": text,
		})
	}

	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ports.ErrTimeout
		}
		return fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	return nil
}
