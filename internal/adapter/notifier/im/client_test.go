package im_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/notifier/im"
)

func TestIMNotificationDelivery(t *testing.T) {
	received := make(map[string]string)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		received[path] = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := im.NewClient(server.Client())
	ctx := context.Background()

	// 1. WeChat Work
	err := client.SendNotification(ctx, "wechat", server.URL+"/wechat", "入库完成", "《绝命毒师》全季入库完成")
	if err != nil || !strings.Contains(received["/wechat"], "绝命毒师") {
		t.Fatalf("wechat notification failed: %v", err)
	}

	// 2. DingTalk
	err = client.SendNotification(ctx, "dingtalk", server.URL+"/dingtalk", "开始下载", "种子已添加至 qBittorrent")
	if err != nil || !strings.Contains(received["/dingtalk"], "qBittorrent") {
		t.Fatalf("dingtalk notification failed: %v", err)
	}

	// 3. Feishu
	err = client.SendNotification(ctx, "feishu", server.URL+"/feishu", "订阅新增", "新增电影订阅《盗梦空间》")
	if err != nil || !strings.Contains(received["/feishu"], "盗梦空间") {
		t.Fatalf("feishu notification failed: %v", err)
	}

	// 4. Bark
	err = client.SendNotification(ctx, "bark", server.URL+"/bark", "系统告警", "做种站点限流")
	if err != nil || !strings.Contains(received["/bark"], "系统告警") {
		t.Fatalf("bark notification failed: %v", err)
	}
}
