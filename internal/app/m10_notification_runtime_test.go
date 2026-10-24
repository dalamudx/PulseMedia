package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

type robotNativeTransport func(*http.Request) (*http.Response, error)

func (f robotNativeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestM10NotificationNativeAppRuntimePG(t *testing.T) {
	if !strings.HasPrefix(os.Getenv("TMPDIR"), "/data/temp/") {
		t.Fatal("owned temp required")
	}
	f := organizationSetup(t)
	_, _, dispatcher := testOutboxServices(f)
	admin := notificationFixtureAdmin(t, f)
	var mu sync.Mutex
	calls := map[string]int{}
	events := map[string]string{}
	failed := true
	bad := make(chan string, 32)
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := r.Header.Get("X-Owned-Channel")
		mu.Lock()
		calls[kind]++
		count := calls[kind]
		fail := failed && kind == "dingtalk"
		event := r.Header.Get("X-PulseMedia-Event-ID")
		if previous := events[kind]; previous != "" && event != previous {
			bad <- "replay changed event"
		}
		events[kind] = event
		mu.Unlock()
		if kind == "telegram" {
			var p map[string]any
			if json.NewDecoder(r.Body).Decode(&p) != nil || p["chat_id"] != "owned-chat" {
				bad <- "telegram payload mismatch"
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var p map[string]any
		if json.Unmarshal(raw, &p) != nil || !strings.Contains(string(raw), "媒体入库记录") || !strings.Contains(string(raw), event) || strings.Contains(string(raw), f.target) || strings.Contains(string(raw), "owned-private") {
			bad <- "unsafe or incomplete native card"
		}
		switch kind {
		case "wechat":
			if p["msgtype"] != "news" || !strings.Contains(string(raw), "https://image.tmdb.org/t/p/w342/owned-poster.jpg") {
				bad <- "wechat shape"
			}
		case "dingtalk":
			if p["msgtype"] != "markdown" || r.URL.Query().Get("sign") == "" || r.URL.Query().Get("timestamp") == "" || !strings.Contains(string(raw), "![封面](https://image.tmdb.org/t/p/w342/owned-poster.jpg)") {
				bad <- "dingtalk shape/signature"
			}
		case "feishu":
			if p["msg_type"] != "interactive" || p["card"] == nil || p["sign"] == nil || !strings.Contains(string(raw), "img_owned-poster") {
				bad <- "feishu shape/signature"
			}
		case "bark":
			if p["group"] != "PulseMedia" || p["icon"] != "https://image.tmdb.org/t/p/w342/owned-poster.jpg" {
				bad <- "bark group"
			}
		default:
			bad <- "unknown destination"
		}
		if fail {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"errcode":130101,"errmsg":"owned-private-response"}`)
			return
		}
		switch kind {
		case "wechat", "dingtalk":
			_, _ = io.WriteString(w, `{"errcode":0}`)
		case "feishu":
			_, _ = io.WriteString(w, `{"code":0}`)
		default:
			_, _ = io.WriteString(w, `{"code":200}`)
		}
		if kind != "dingtalk" && count != 1 {
			bad <- "successful recipient repeated"
		}
	}))
	defer remote.Close()
	client := remote.Client()
	transport := client.Transport
	target, _ := url.Parse(remote.URL)
	client.Transport = robotNativeTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		u := *r.URL
		kind := ""
		switch u.Host {
		case "qyapi.weixin.qq.com":
			kind = "wechat"
		case "oapi.dingtalk.com":
			kind = "dingtalk"
		case "open.feishu.cn":
			kind = "feishu"
		case "push.example":
			kind = "bark"
		case "api.telegram.org":
			kind = "telegram"
		default:
			return nil, errors.New("unexpected owned target")
		}
		u.Scheme = target.Scheme
		u.Host = target.Host
		copy.URL = &u
		copy.Header.Set("X-Owned-Channel", kind)
		return transport.RoundTrip(copy)
	})
	channels := map[string]uuid.UUID{}
	destinations := map[string]string{"wechat": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=owned-private", "dingtalk": "https://oapi.dingtalk.com/robot/send?access_token=owned-private", "feishu": "https://open.feishu.cn/open-apis/bot/v2/hook/owned-private", "bark": "https://push.example/owned-private", "telegram": ""}
	for kind, destination := range destinations {
		c := notify.ChannelConfig{URL: destination}
		switch kind {
		case "wechat", "dingtalk", "bark":
			c.ImageURL = "https://image.tmdb.org/t/p/w342/owned-poster.jpg"
		case "feishu":
			c.ImageKey = "img_owned-poster"
		}
		if kind == "dingtalk" || kind == "feishu" {
			c.Secret = "owned-private-signing"
		}
		if kind == "telegram" {
			c.BotToken = "owned-private-token"
			c.ChatID = "owned-chat"
		}
		channel, e := dispatcher.CreateChannel(t.Context(), admin, "owned "+kind, kind, c, "owned-create-"+kind)
		if e != nil {
			t.Fatal(e)
		}
		channels[kind] = channel.ID
	}
	f.observe(t)
	if e := f.svc.OrganizeDownload(t.Context(), f.claim(t, "notification-importer")); e != nil {
		t.Fatal(e)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	runtimePool, e := postgres.NewPool(t.Context(), cfg.Database.URL, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	application, e := New(cfg, WithDBPool(runtimePool), WithNotificationHTTPClient(client), WithLogger(logging.NewLogger(io.Discard, "error")))
	if e != nil {
		runtimePool.Close()
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	waitOutboxCondition(t, ctx, func() bool {
		var good int
		var state string
		var due time.Time
		if e := f.pool.QueryRow(ctx, `SELECT count(*) FROM notification_batch b JOIN outbox_delivery d ON d.event_id=b.event_id WHERE b.channel_id<>$1 AND d.state='delivered'`, channels["dingtalk"]).Scan(&good); e != nil || good != 4 {
			return false
		}
		e := f.pool.QueryRow(ctx, `SELECT d.state,d.not_before FROM notification_batch b JOIN outbox_delivery d ON d.event_id=b.event_id WHERE b.channel_id=$1`, channels["dingtalk"]).Scan(&state, &due)
		return e == nil && state == "pending" && due.After(time.Now().Add(100*time.Second))
	})
	f.assertImported(t)
	mu.Lock()
	if calls["dingtalk"] != 1 {
		t.Error("rate-limited recipient repeated early")
	}
	failed = false
	mu.Unlock()
	// Only the controlled test clock/deadline is advanced, without touching authority.
	if _, e = f.pool.Exec(ctx, `UPDATE outbox_delivery SET not_before=clock_timestamp()-interval '1 second' WHERE consumer=$1 AND state='pending'`, "notify:"+channels["dingtalk"].String()); e != nil {
		t.Fatal(e)
	}
	waitOutboxCondition(t, ctx, func() bool {
		var count int
		e := f.pool.QueryRow(ctx, `SELECT count(*) FROM notification_batch b JOIN outbox_delivery d ON d.event_id=b.event_id WHERE d.state='delivered'`).Scan(&count)
		return e == nil && count == 5
	})
	mu.Lock()
	for kind, count := range calls {
		want := 1
		if kind == "dingtalk" {
			want = 2
		}
		if count != want {
			t.Error("wrong native request count", kind, count)
		}
	}
	mu.Unlock()
	select {
	case reason := <-bad:
		t.Fatal(reason)
	default:
	}
	var leaked bool
	if e = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_log WHERE payload::text LIKE '%owned-private%') OR EXISTS(SELECT 1 FROM idempotency_record WHERE response_body::text LIKE '%owned-private%') OR EXISTS(SELECT 1 FROM outbox_delivery WHERE error LIKE '%owned-private%')`).Scan(&leaked); e != nil || leaked {
		t.Fatal("private data in success/error ledger", e)
	}
	f.assertImported(t)
	cancel()
	e = <-done
	joined = true
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	t.Log("M10_NOTIFICATION_NATIVE_APP_PASS channels=FIVE native=TLS_OWNED import=REAL_HARDLINK recipient_failure=ISOLATED RetryAfter=PERSISTED original_event=UNCHANGED credentials=ENCRYPTED_AND_REDACTED")
}
