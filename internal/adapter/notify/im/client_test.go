package im

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var destinations = map[string]string{"wechat": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=owned", "dingtalk": "https://oapi.dingtalk.com/robot/send?access_token=owned", "feishu": "https://open.feishu.cn/open-apis/bot/v2/hook/owned-key", "bark": "https://push.example/owned-key"}

func testMessage() ports.Notification {
	return ports.Notification{EventID: "owned-event", Title: "入库完成", Body: "文件已整理；保留原始文件", Timestamp: time.Unix(1700000000, 0)}
}
func TestRobotPayloadAndSigning(t *testing.T) {
	for kind, destination := range destinations {
		t.Run(kind, func(t *testing.T) {
			secret := ""
			if kind == "dingtalk" || kind == "feishu" {
				secret = "owned-signing-secret"
			}
			now := time.Unix(1700000000, 123000000)
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-PulseMedia-Event-ID") != "owned-event" {
					t.Error("wrong request identity")
				}
				raw, _ := io.ReadAll(r.Body)
				var p map[string]any
				if json.Unmarshal(raw, &p) != nil {
					t.Error("bad JSON")
				}
				if !strings.Contains(string(raw), "入库完成") || !strings.Contains(string(raw), "owned-event") {
					t.Error("missing card content")
				}
				switch kind {
				case "wechat":
					if p["msgtype"] != "markdown" || p["markdown"] == nil {
						t.Error("wrong WeChat card")
					}
				case "dingtalk":
					if p["msgtype"] != "markdown" || p["markdown"] == nil {
						t.Error("wrong DingTalk card")
					}
					stamp := strconv.FormatInt(now.UnixMilli(), 10)
					mac := hmac.New(sha256.New, []byte(secret))
					_, _ = io.WriteString(mac, stamp+"\n"+secret)
					if r.URL.Query().Get("timestamp") != stamp || r.URL.Query().Get("sign") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
						t.Error("bad millisecond signature")
					}
				case "feishu":
					stamp := strconv.FormatInt(now.Unix(), 10)
					mac := hmac.New(sha256.New, []byte(stamp+"\n"+secret))
					if p["msg_type"] != "interactive" || p["card"] == nil || p["timestamp"] != stamp || p["sign"] != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
						t.Error("bad Feishu card or signature")
					}
				case "bark":
					if p["group"] != "PulseMedia" || p["body"] == nil || r.URL.Path != "/owned-key" {
						t.Error("bad Bark body")
					}
				}
				if strings.Contains(string(raw), secret) && secret != "" {
					t.Error("secret in message")
				}
				switch kind {
				case "wechat", "dingtalk":
					_, _ = io.WriteString(w, `{"errcode":0}`)
				case "feishu":
					_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
				case "bark":
					_, _ = io.WriteString(w, `{"code":200,"message":"success"}`)
				}
			}))
			defer server.Close()
			base := server.Client()
			transport := base.Transport
			target, _ := url.Parse(server.URL)
			base.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				u := *r.URL
				u.Scheme = target.Scheme
				u.Host = target.Host
				r.URL = &u
				return transport.RoundTrip(r)
			})
			client := NewClient(kind, destination, secret, base, clock.NewMock(now))
			if e := client.Send(t.Context(), testMessage()); e != nil {
				t.Fatal(e)
			}
			if calls.Load() != 1 {
				t.Fatal("not exactly one POST")
			}
		})
	}
}
func TestRobotAcknowledgementFailures(t *testing.T) {
	for kind, destination := range destinations {
		for _, body := range []string{"", `{}`, `null`, `[]`, `{"errcode":null,"code":null}`, `{"errcode":1,"code":1}`, `{"errcode":"0","code":"0"}`, `{"errcode":0,"errcode":1,"code":0}`, `{"code":0,"code":200}`, `{"errcode":0,"code":0} trailing`, strings.Repeat("x", 65537)} {
			t.Run(kind+"/"+fmt.Sprint(len(body))+"/"+body[:min(len(body), 20)], func(t *testing.T) {
				var calls int
				h := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}
				e := NewClient(kind, destination, "", h, nil).Send(t.Context(), testMessage())
				if e == nil || calls != 1 {
					t.Fatal("invalid acknowledgement delivered", e, calls)
				}
			})
		}
	}
}
func TestRobotStatusAndRetryAfter(t *testing.T) {
	for _, c := range []struct {
		status int
		header string
		want   error
		delay  time.Duration
	}{{401, "", ports.ErrAuthRequired, 0}, {403, "", ports.ErrAuthRequired, 0}, {500, "", ports.ErrUnavailable, 0}, {302, "", ports.ErrBadResponse, 0}, {204, "", ports.ErrBadResponse, 0}, {429, "91", nil, 91 * time.Second}, {429, "bad", nil, time.Minute}} {
		h := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: c.status, Header: http.Header{"Retry-After": []string{c.header}}, Body: io.NopCloser(strings.NewReader("private-response"))}, nil
		})}
		e := NewClient("wechat", destinations["wechat"], "", h, nil).Send(t.Context(), testMessage())
		if e == nil || (c.delay == 0 && !errors.Is(e, c.want)) || strings.Contains(e.Error(), "private") {
			t.Fatal(e)
		}
		if c.delay > 0 {
			r, ok := ports.IsRateLimited(e)
			if !ok || r.RetryAfter != c.delay {
				t.Fatal(e)
			}
		}
	}
}
func TestRobotCancellationAndInvalidInputNoIO(t *testing.T) {
	var calls int
	h := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("private-url") })}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if e := NewClient("wechat", destinations["wechat"], "", h, nil).Send(ctx, testMessage()); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	for _, bad := range []struct {
		kind, url string
		n         ports.Notification
	}{{"unknown", destinations["wechat"], testMessage()}, {"wechat", "http://bad.invalid/key", testMessage()}, {"wechat", destinations["wechat"], ports.Notification{}}, {"wechat", destinations["wechat"], ports.Notification{EventID: "a\nb"}}, {"wechat", destinations["wechat"], ports.Notification{EventID: "a", Body: strings.Repeat("x", 2001)}}} {
		if e := NewClient(bad.kind, bad.url, "", h, nil).Send(t.Context(), bad.n); !errors.Is(e, ports.ErrRejected) {
			t.Fatal(e)
		}
	}
	if calls != 0 {
		t.Fatal("invalid input made network requests")
	}
}
func TestRobotRedirectAndClientIsolation(t *testing.T) {
	var seen atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Add(1) }))
	defer target.Close()
	h := target.Client()
	original := h.Transport
	u, _ := url.Parse(target.URL)
	h.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "qyapi.weixin.qq.com" {
			return original.RoundTrip(r)
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{u.String()}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	client := NewClient("wechat", destinations["wechat"], "", h, nil)
	if e := client.Send(t.Context(), testMessage()); !errors.Is(e, ports.ErrBadResponse) {
		t.Fatal(e)
	}
	if seen.Load() != 0 || h.CheckRedirect != nil || h.Timeout != 0 || client.http.Timeout != 10*time.Second {
		t.Fatal("redirected credentials or mutated borrowed client")
	}
}
func TestRobotTransportErrorRedaction(t *testing.T) {
	for _, cause := range []error{errors.New("https://qyapi.weixin.qq.com/private-key"), context.DeadlineExceeded, context.Canceled} {
		h := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, cause })}
		e := NewClient("wechat", destinations["wechat"], "", h, nil).Test(t.Context())
		if e == nil || strings.Contains(e.Error(), "private-key") {
			t.Fatal(e)
		}
	}
}
func TestRobotNativeRateLimits(t *testing.T) {
	for _, c := range []struct{ kind, body string }{{"wechat", `{"errcode":45009}`}, {"dingtalk", `{"errcode":130101}`}, {"feishu", `{"code":11232}`}} {
		h := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(c.body))}, nil
		})}
		if _, ok := ports.IsRateLimited(NewClient(c.kind, destinations[c.kind], "", h, nil).Test(t.Context())); !ok {
			t.Fatal("business limit not retained")
		}
	}
}
