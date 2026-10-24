package im

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/adapter/notify/transport"
	rules "github.com/dalamudx/PulseMedia/internal/domain/notification"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Client owns one immutable destination. The persistent outbox owns retry policy.
type Artwork struct{ URL, Key string }

type Client struct {
	kind, destination, secret string
	artwork                   Artwork
	http                      *http.Client
	clock                     ports.Clock
}

var _ ports.Notifier = (*Client)(nil)

func NewClient(kind, destination, secret string, h *http.Client, clock ports.Clock, artwork ...Artwork) *Client {
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Second}
	}
	clone := *h
	if clone.Timeout <= 0 || clone.Timeout > 10*time.Second {
		clone.Timeout = 10 * time.Second
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	a := Artwork{}
	if len(artwork) > 0 {
		a = artwork[0]
	}
	return &Client{kind: kind, destination: destination, secret: secret, http: &clone, clock: clock, artwork: a}
}
func (c *Client) now() time.Time {
	if c.clock != nil {
		return c.clock.Now()
	}
	return time.Now()
}
func (c *Client) Send(ctx context.Context, n ports.Notification) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if (!rules.ValidRobot(c.kind, c.destination, c.secret) || !rules.ValidArtwork(c.kind, c.artwork.URL, c.artwork.Key)) || n.EventID == "" || len(n.EventID) > 160 || strings.ContainsAny(n.EventID, "\r\n") || !utf8.ValidString(n.Title+n.Body) || len(n.Title) > 200 || len(n.Body) > 2000 {
		return ports.ErrRejected
	}
	destination, payload := c.payload(n)
	raw, e := json.Marshal(payload)
	if e != nil || len(raw) > 8192 {
		return ports.ErrRejected
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, destination, bytes.NewReader(raw))
	if e != nil {
		return ports.ErrRejected
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PulseMedia-Event-ID", n.EventID)
	resp, e := c.http.Do(req)
	if e != nil {
		return transport.Error(ctx, e)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ports.ErrAuthRequired
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return c.rateLimit(resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode >= 500 {
		return ports.ErrUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return ports.ErrBadResponse
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if e != nil {
		return transport.Error(ctx, e)
	}
	if len(body) > 65536 {
		return ports.ErrBadResponse
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(body, &result) != nil || result == nil {
		return ports.ErrBadResponse
	}
	field, want := "errcode", int64(0)
	if c.kind == "feishu" {
		field = "code"
	}
	if c.kind == "bark" {
		field = "code"
		want = 200
	}
	// Duplicate acknowledgement keys must never turn rejection into delivery.
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, e = dec.Token(); e != nil {
		return ports.ErrBadResponse
	}
	seen := map[string]bool{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return ports.ErrBadResponse
		}
		key, ok := k.(string)
		key = strings.ToLower(key)
		if !ok || seen[key] {
			return ports.ErrBadResponse
		}
		seen[key] = true
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return ports.ErrBadResponse
		}
	}
	var code int64
	if v, ok := result[field]; !ok || string(v) == "null" || json.Unmarshal(v, &code) != nil {
		return ports.ErrBadResponse
	}
	if code == want {
		return nil
	}
	if c.kind == "wechat" && code == 45009 || c.kind == "dingtalk" && code == 130101 || c.kind == "feishu" && code == 11232 {
		return c.rateLimit("")
	}
	return ports.ErrRejected
}
func (c *Client) rateLimit(value string) error {
	delay := time.Minute
	if v, e := strconv.ParseInt(value, 10, 32); e == nil && v > 0 && v <= 86400 {
		delay = time.Duration(v) * time.Second
	} else if v, e := http.ParseTime(value); e == nil {
		if d := v.Sub(c.now()); d > 0 && d <= 24*time.Hour {
			delay = d
		}
	}
	return ports.NewRateLimitError(delay, "notification channel rate limited")
}
func (c *Client) payload(n ports.Notification) (string, map[string]any) {
	body := n.Body + "\n\n事件 ID: " + n.EventID
	destination := c.destination
	var p map[string]any
	switch c.kind {
	case "wechat":
		p = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": "### " + n.Title + "\n" + body}}
		if c.artwork.URL != "" {
			p = map[string]any{"msgtype": "news", "news": map[string]any{"articles": []any{map[string]string{"title": n.Title, "description": body, "url": c.artwork.URL, "picurl": c.artwork.URL}}}}
		}
	case "dingtalk":
		if c.artwork.URL != "" {
			body = "![封面](" + c.artwork.URL + ")\n\n" + body
		}
		p = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": n.Title, "text": "### " + n.Title + "\n\n" + body}}
		if c.secret != "" {
			u, _ := url.Parse(destination)
			q := u.Query()
			stamp := strconv.FormatInt(c.now().UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(c.secret))
			_, _ = mac.Write([]byte(stamp + "\n" + c.secret))
			q.Set("timestamp", stamp)
			q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			u.RawQuery = q.Encode()
			destination = u.String()
		}
	case "feishu":
		elements := []any{map[string]string{"tag": "markdown", "content": body}}
		if c.artwork.Key != "" {
			elements = append(elements, map[string]any{"tag": "img", "img_key": c.artwork.Key, "alt": map[string]string{"tag": "plain_text", "content": "通知封面"}})
		}
		p = map[string]any{"msg_type": "interactive", "card": map[string]any{"header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": n.Title}}, "elements": elements}}
		if c.secret != "" {
			stamp := strconv.FormatInt(c.now().Unix(), 10)
			mac := hmac.New(sha256.New, []byte(stamp+"\n"+c.secret))
			p["timestamp"] = stamp
			p["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
	case "bark":
		p = map[string]any{"title": n.Title, "body": body, "group": "PulseMedia"}
		if c.artwork.URL != "" {
			p["icon"] = c.artwork.URL
		}
	}
	return destination, p
}
func (c *Client) Test(ctx context.Context) error {
	return c.Send(ctx, ports.Notification{EventID: "test", Title: "PulseMedia 测试通知", Body: "用于检查通知通道连通性", Timestamp: c.now()})
}
