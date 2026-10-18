package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNotificationBatchAppRunRestoresOpenWindow(t *testing.T) {
	f := organizationSetup(t)
	_, _, n := testOutboxServices(f)
	receipts := make(chan []byte, 4)
	bad := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("private-hmac-secret"))
		mac.Write(raw)
		var body ports.Notification
		if json.Unmarshal(raw, &body) != nil || body.EventID != r.Header.Get("X-PulseMedia-Event-ID") || r.Header.Get("X-PulseMedia-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			bad <- "invalid receipt identity/signature"
		}
		receipts <- raw
		w.WriteHeader(204)
	}))
	defer server.Close()
	ch := createOutboxChannel(t, f, n, server.URL)
	f.observe(t)
	if e := f.svc.OrganizeDownload(context.Background(), f.claim(t, "stopped-importer")); e != nil {
		t.Fatal(e)
	}
	var state string
	var created, deadline time.Time
	if e := f.pool.QueryRow(context.Background(), "SELECT state,created_at,deadline FROM notification_batch WHERE channel_id=$1", ch).Scan(&state, &created, &deadline); e != nil || state != "open" || deadline.Sub(created) > 3100*time.Millisecond {
		t.Fatal("window is not persisted/fixed", state, e)
	}
	if f.count(t, "outbox_delivery") != 0 {
		t.Fatal("unsealed batch sent before restart")
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, e := New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	waitOutboxCondition(t, ctx, func() bool {
		var ready bool
		e := f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM notification_batch b JOIN outbox_delivery d ON d.event_id=b.event_id WHERE b.channel_id=$1 AND b.state='sealed' AND d.state='delivered')", ch).Scan(&ready)
		return e == nil && ready
	})
	select {
	case reason := <-bad:
		t.Fatal(reason)
	default:
	}
	if len(receipts) != 1 {
		t.Fatal("duplicate notification after restored window", len(receipts))
	}
	raw := <-receipts
	var body ports.Notification
	if json.Unmarshal(raw, &body) != nil || body.Topic != "notification.media" || !strings.Contains(body.Body, "1 个单元，1 个文件，1 个目标库") || strings.Contains(string(raw), f.target) || strings.Contains(string(raw), "private-") {
		t.Fatal("wrong localized/private content", string(raw))
	}
	var eventID string
	if e = f.pool.QueryRow(context.Background(), "SELECT event_id::text FROM notification_batch WHERE channel_id=$1", ch).Scan(&eventID); e != nil || eventID != body.EventID {
		t.Fatal("app did not send sealed persisted event", e)
	}
	f.assertImported(t)
	cancel()
	e = <-done
	joined = true
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
