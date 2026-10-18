package webpush_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/notifier/webpush"
)

func TestWebPushDispatch(t *testing.T) {
	var receivedBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := webpush.NewClient(server.Client())
	ctx := context.Background()

	err := client.SendNotification(ctx, server.URL, webpush.NotificationPayload{
		Title: "影视已入库",
		Body:  "《星际穿越》4K Remux 已成功硬链接入库并刷新 Plex",
	})
	if err != nil {
		t.Fatalf("send webpush failed: %v", err)
	}

	if !strings.Contains(receivedBody, "星际穿越") {
		t.Fatalf("unexpected body: %s", receivedBody)
	}
}
