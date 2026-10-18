package telegram_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/telegram"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundtrip func(*http.Request) (*http.Response, error)

func (f roundtrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTelegramRequiresActualOKAndBoundsResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
		retry      time.Duration
	}{{"accepted", `{"ok":true}`, 200, nil, 0}, {"false", `{"ok":false,"description":"private-bot-secret"}`, 200, ports.ErrBadResponse, 0}, {"empty", "", 200, ports.ErrBadResponse, 0}, {"malformed", "private-bot-secret", 200, ports.ErrBadResponse, 0}, {"oversize", `{"ok":true,"description":"` + strings.Repeat("x", 65536) + `"}`, 200, ports.ErrBadResponse, 0}, {"rate", `{"ok":false,"parameters":{"retry_after":90},"description":"private-bot-secret"}`, 429, nil, 90 * time.Second}, {"offline", "private-bot-secret", 503, ports.ErrUnavailable, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			client := telegram.NewClient("123:private-bot-secret", "@testchannel", &http.Client{Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), "parse_mode") || !strings.Contains(string(b), "stable-event-id") {
					t.Error("missing stable event or unescaped Markdown enabled")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})})
			err := client.Send(context.Background(), ports.Notification{EventID: "stable-event-id", Title: "Title_with [syntax]"})
			if tc.retry > 0 {
				var rate *ports.RateLimitError
				if !errors.As(err, &rate) || rate.RetryAfter != tc.retry {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("false delivery accepted: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-") {
				t.Fatal("private Telegram error escaped")
			}
		})
	}
}
