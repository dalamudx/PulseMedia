package notify_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/telegram"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/webhook"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"testing"
)

type trip func(*http.Request) (*http.Response, error)

func (f trip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeout struct{}

func (timeout) Error() string {
	return "https://private-destination.test/private-token: private-secret"
}
func (timeout) Timeout() bool   { return true }
func (timeout) Temporary() bool { return true }
func TestNativeNotificationSafeFailureClasses(t *testing.T) {
	builders := map[string]func(*http.Client) ports.Notifier{"webhook": func(h *http.Client) ports.Notifier {
		return webhook.NewClient("https://private-destination.test/?token=private-secret", "private-secret", h)
	}, "telegram": func(h *http.Client) ports.Notifier {
		return telegram.NewClient("123:private-secret", "private-chat", h)
	}}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name            string
				err, errorClass error
			}{{"transport", errors.New("private-secret"), ports.ErrUnavailable}, {"cancel", context.Canceled, context.Canceled}, {"deadline", context.DeadlineExceeded, ports.ErrTimeout}, {"network timeout", timeout{}, ports.ErrTimeout}} {
				t.Run(tc.name, func(t *testing.T) {
					e := build(&http.Client{Transport: trip(func(*http.Request) (*http.Response, error) { return nil, tc.err })}).Send(context.Background(), ports.Notification{EventID: "stable"})
					if !errors.Is(e, tc.errorClass) || strings.Contains(e.Error(), "private-") {
						t.Fatal("unsafe or wrong transport class", e)
					}
				})
			}
			for _, tc := range []struct {
				status int
				want   error
			}{{401, ports.ErrAuthRequired}, {403, ports.ErrAuthRequired}, {503, ports.ErrUnavailable}, {302, ports.ErrBadResponse}} {
				e := build(&http.Client{Transport: trip(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("private-secret")), Header: make(http.Header)}, nil
				})}).Send(context.Background(), ports.Notification{EventID: "stable"})
				if !errors.Is(e, tc.want) || strings.Contains(e.Error(), "private-") {
					t.Fatal("unsafe or wrong HTTP class", tc.status, e)
				}
			}
		})
	}
	if e := webhook.NewClient("https://private-secret/%zz", "private-secret", nil).Send(context.Background(), ports.Notification{}); !errors.Is(e, ports.ErrBadResponse) || strings.Contains(e.Error(), "private-") {
		t.Fatal("request construction exposed URL", e)
	}
}
