package site_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/adapter/siteattendance"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
)

func TestSiteCheckinAndTrafficAnalytics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "valid_pt_cookie=123" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/attendance.php" {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `<html><body>这是您的第 42 次签到，获得魔力值 50</body></html>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	svc := site.NewService(siteattendance.NewClient(server.Client()))
	ctx := context.Background()

	// 1. Checkin succeeds with valid cookie
	res, err := svc.CheckinSite(ctx, server.URL, "valid_pt_cookie=123")
	if err != nil || !res.Success || res.Message != "签到成功，已记录连续签到天数" {
		t.Fatalf("checkin failed: %v, %+v", err, res)
	}

	// 2. Checkin fails with invalid cookie
	resBad, err := svc.CheckinSite(ctx, server.URL, "bad_cookie")
	if err == nil || resBad.Success {
		t.Fatal("expected auth error with invalid cookie")
	}

	// 3. Traffic statistics
	stats, err := svc.GetTrafficStats(ctx, uuid.New())
	if !errors.Is(err, ports.ErrUnavailable) || stats != nil {
		t.Fatalf("unconfigured stats must be unavailable: %v, %+v", err, stats)
	}
}
