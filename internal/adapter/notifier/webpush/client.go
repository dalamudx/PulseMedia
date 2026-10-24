package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	native "github.com/SherClockHolmes/webpush-go"
	pushrules "github.com/dalamudx/PulseMedia/internal/domain/webpush"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type NotificationPayload = ports.WebPushPayload

// Subscription contains browser-generated secrets, never a public response DTO.
type Subscription = ports.WebPushSubscription

type Config struct {
	PublicKey  string
	PrivateKey string
	Subject    string
}

type Client struct {
	httpClient *http.Client
	config     Config
	now        func() time.Time
}

// NewClient freezes a validated VAPID identity and an explicitly supplied clock.
func NewClient(config Config, httpClient *http.Client, now func() time.Time) (*Client, error) {
	secret, ok := decodeKey(config.PrivateKey, 32)
	if !ok || now == nil {
		return nil, ports.ErrRejected
	}
	private, err := ecdh.P256().NewPrivateKey(secret)
	if err != nil {
		return nil, ports.ErrRejected
	}
	public, ok := decodeKey(config.PublicKey, 65)
	if !ok || !bytes.Equal(private.PublicKey().Bytes(), public) {
		return nil, ports.ErrRejected
	}
	subject, ok := contact(config.Subject)
	if !ok {
		return nil, ports.ErrRejected
	}
	config.Subject = subject
	client := http.Client{Timeout: 10 * time.Second}
	if httpClient != nil {
		client = *httpClient
	}
	if client.Timeout <= 0 || client.Timeout > 10*time.Second {
		client.Timeout = 10 * time.Second
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &client, config: config, now: now}, nil
}

func ValidateSubscription(subscription Subscription) error {
	if !pushrules.ValidSubscription(subscription) {
		return ports.ErrRejected
	}
	return nil
}

// SendNotification makes one encrypted POST. Acceptance means queued by the push
// service, not that a browser has displayed a notification. Recovery is owned by
// the delivery ledger; this adapter never follows redirects or retries.
func (c *Client) SendNotification(ctx context.Context, subscription Subscription, payload NotificationPayload) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateSubscription(subscription); err != nil {
		return err
	}
	if !validPayload(payload) {
		return ports.ErrRejected
	}
	data, err := json.Marshal(payload)
	if err != nil || len(data) > 3000 {
		return ports.ErrRejected
	}
	now := c.now()
	if now.IsZero() || now.Year() < 2000 || now.Year() > 9998 {
		return ports.ErrRejected
	}
	endpoint, err := url.Parse(subscription.Endpoint)
	if err != nil {
		return ports.ErrRejected
	}
	// RFC6454 origin serialization omits the default HTTPS port.
	endpoint.Host = endpoint.Hostname()
	response, err := native.SendNotificationWithContext(ctx, data, &native.Subscription{
		Endpoint: endpoint.String(), Keys: native.Keys{P256dh: subscription.P256DH, Auth: subscription.Auth},
	}, &native.Options{
		HTTPClient: c.httpClient, RecordSize: 4096, TTL: 300,
		Subscriber: c.config.Subject, VAPIDPublicKey: c.config.PublicKey,
		VAPIDPrivateKey: c.config.PrivateKey, VapidExpiration: now.Add(time.Hour),
	})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ports.ErrTimeout
		}
		return ports.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusCreated:
		return nil
	case http.StatusNotFound, http.StatusGone:
		return ports.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return ports.ErrAuthRequired
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return ports.ErrRejected
	case http.StatusTooManyRequests:
		return ports.NewRateLimitError(retryAfter(response.Header.Get("Retry-After"), now), "web push service throttled")
	default:
		if response.StatusCode >= 500 {
			return ports.ErrUnavailable
		}
		return ports.ErrBadResponse
	}
}

func decodeKey(value string, size int) ([]byte, bool) {
	if len(value) != base64.RawURLEncoding.EncodedLen(size) {
		return nil, false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return decoded, err == nil && len(decoded) == size
}

func contact(value string) (string, bool) {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value {
		return "", false
	}
	if strings.HasPrefix(value, "https://") {
		u, err := url.Parse(value)
		return value, err == nil && u.Hostname() != "" && u.User == nil && u.Fragment == ""
	}
	value = strings.TrimPrefix(value, "mailto:")
	address, err := mail.ParseAddress(value)
	return value, err == nil && address.Address == value && address.Name == "" && strings.Contains(value, "@")
}

func validPayload(payload NotificationPayload) bool {
	for _, value := range []string{payload.Title, payload.Body, payload.Icon, payload.URL, payload.Tag} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	return strings.TrimSpace(payload.Title) != "" && len(payload.Title) <= 160 && len(payload.Body) <= 1800 && len(payload.Tag) <= 128 && localPath(payload.Icon) && localPath(payload.URL)
}

func localPath(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 512 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && !u.IsAbs() && u.Host == "" && u.Fragment == "" && !strings.HasPrefix(u.Path, "//") && !strings.Contains(u.Path, "\\")
}

func retryAfter(value string, now time.Time) time.Duration {
	delay := 30 * time.Second
	if len(value) <= 64 {
		if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
			delay = time.Duration(seconds) * time.Second
		} else if date, err := http.ParseTime(value); err == nil && date.After(now) {
			delay = date.Sub(now)
		}
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}
