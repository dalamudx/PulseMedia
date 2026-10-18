package app_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/cookiecloud"
	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/emby"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/bangumi"
	"github.com/dalamudx/PulseMedia/internal/adapter/notifier/im"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
)

func TestFullM10EcosystemIntegration(t *testing.T) {
	ctx := context.Background()

	// 1. CookieCloud Sync
	cookieServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"cookie_data": {"kp.m-team.cc": [{"name": "tp", "value": "token_123"}]}}`)
	}))
	defer cookieServer.Close()

	ccClient := cookiecloud.NewClient(cookieServer.Client())
	cookies, err := ccClient.SyncCookies(ctx, cookieServer.URL, "user-uuid", "password")
	if err != nil || cookies["kp.m-team.cc"] != "tp=token_123" {
		t.Fatalf("cookiecloud sync failed: %v, %+v", err, cookies)
	}

	// 2. Bangumi Calendar
	bgmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"weekday": {"id": 1, "cn": "星期一"}, "items": [{"id": 1, "name_cn": "葬送的芙莉莲", "air_date": "2023-09-29"}]}]`)
	}))
	defer bgmServer.Close()

	bgmClient := bangumi.NewClient(bgmServer.URL, bgmServer.Client())
	sched, err := bgmClient.GetCalendar(ctx)
	if err != nil || len(sched) != 1 || sched[0].Items[0].Title != "葬送的芙莉莲" {
		t.Fatalf("bangumi calendar failed: %v, %+v", err, sched)
	}

	// 3. Seeding Policy: Auto removal on ratio or time
	rule := download.SeedingRule{
		MaxSeedingHours: 48,
		MinRatio:        2.0,
		DeleteFiles:     false,
	}
	now := time.Now()
	res := download.EvaluateSeedingPolicy(&ports.TorrentInfo{
		InfoHash:        "hash",
		DownloadedBytes: 1000,
		UploadedBytes:   2500, // ratio 2.5 >= 2.0
	}, now.Add(-1*time.Hour), now, rule)
	if !res.ShouldRemove {
		t.Fatalf("expected should remove due to ratio: %+v", res)
	}

	// 4. Emby Media Server Client
	embyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/System/Info" {
			_, _ = fmt.Fprint(w, `{"ServerName": "Emby Main"}`)
			return
		}
		if r.URL.Path == "/Library/Refresh" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer embyServer.Close()

	embyClient := emby.NewClient(embyServer.URL, "token", embyServer.Client())
	if err := embyClient.Test(ctx); err != nil {
		t.Fatalf("emby test failed: %v", err)
	}
	if err := embyClient.RefreshPaths(ctx, []string{"/data/movies"}); err != nil {
		t.Fatalf("emby refresh failed: %v", err)
	}

	// 5. Multi-channel IM Notifier Delivery
	imServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer imServer.Close()

	imClient := im.NewClient(imServer.Client())
	if err := imClient.SendNotification(ctx, "wechat", imServer.URL, "入库完成", "测试正文"); err != nil {
		t.Fatalf("wechat send failed: %v", err)
	}
	if err := imClient.SendNotification(ctx, "dingtalk", imServer.URL, "下载开始", "测试正文"); err != nil {
		t.Fatalf("dingtalk send failed: %v", err)
	}
	if err := imClient.SendNotification(ctx, "bark", imServer.URL, "系统告警", "测试正文"); err != nil {
		t.Fatalf("bark send failed: %v", err)
	}
}
