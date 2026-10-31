package notify_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/notify/im"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/telegram"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/webhook"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestNotificationRetryAfterBounds(t *testing.T) {
	builders := map[string]func(*http.Client) ports.Notifier{
		"webhook": func(h *http.Client) ports.Notifier {
			return webhook.NewClient("https://owned.example/notify", "owned", h)
		},
		"telegram": func(h *http.Client) ports.Notifier { return telegram.NewClient("owned:token", "owned", h) },
	}
	for kind, destination := range map[string]string{"wechat": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=owned", "dingtalk": "https://oapi.dingtalk.com/robot/send?access_token=owned", "feishu": "https://open.feishu.cn/open-apis/bot/v2/hook/owned-key", "bark": "https://push.example/owned-key"} {
		builders[kind] = func(h *http.Client) ports.Notifier {
			secret := ""
			if kind == "dingtalk" || kind == "feishu" {
				secret = "owned-signing-secret"
			}
			return im.NewClient(kind, destination, secret, h, nil)
		}
	}
	for kind, build := range builders {
		for _, tc := range []struct {
			name, header, seconds string
			want                  time.Duration
		}{
			{"normal", "90", "90", 90 * time.Second},
			{"two_days", "172800", "172800", 24 * time.Hour},
			{"int64_max", "9223372036854775807", "9223372036854775807", 24 * time.Hour},
			{"long_date", time.Now().Add(48 * time.Hour).UTC().Format(http.TimeFormat), "172800", 24 * time.Hour},
			{"invalid", "private-invalid", "-1", 0},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				calls := 0
				client := build(&http.Client{Transport: trip(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {tc.header}}, Body: io.NopCloser(strings.NewReader(`{"ok":false,"parameters":{"retry_after":` + tc.seconds + `},"description":"private-body"}`))}, nil
				})})
				err := client.Send(t.Context(), ports.Notification{EventID: "owned-stable-event", Title: "Test", Body: "Test"})
				rate, ok := ports.IsRateLimited(err)
				want := tc.want
				if want == 0 {
					want = time.Minute
					if kind == "telegram" {
						want = 10 * time.Second
					}
				}
				if !ok || rate.RetryAfter != want {
					t.Fatalf("retry=%v want=%s", err, want)
				}
				if calls != 1 || strings.Contains(err.Error(), "private-") {
					t.Fatal("adapter retried or exposed upstream details")
				}
			})
		}
	}
}
