package siteattendance_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/siteattendance"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestAttendanceResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body, message string
		status              int
		success             bool
		want                error
	}{
		{"first", "这是您的第 1 次签到", "签到成功，已记录连续签到天数", 200, true, nil},
		{"success", "签到成功", "签到成功，已记录连续签到天数", 200, true, nil},
		{"already", "已签到", "签到成功，已记录连续签到天数", 200, true, nil},
		{"unknown", "private-body", "站点未确认签到结果", 200, false, nil},
		{"unauthorized", "private-body", "Cookie已失效，需重新登录", 401, false, ports.ErrAuthRequired},
		{"forbidden", "private-body", "Cookie已失效，需重新登录", 403, false, ports.ErrAuthRequired},
		{"status", "private-body", "站点返回状态码 503", 503, false, nil},
		{"oversize", "签到成功" + strings.Repeat("x", 1<<20), "", 200, false, ports.ErrBadResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/attendance.php" || r.Header.Get("Cookie") != "fixture-cookie" || !strings.Contains(r.UserAgent(), "PulseMedia/0.1.0") {
					t.Error("request changed")
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			result, err := siteattendance.NewClient(server.Client()).CheckinSite(context.Background(), server.URL+"/attendance.php", "fixture-cookie")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error class: %v", err)
			}
			if tc.message == "" {
				if result != nil {
					t.Fatal("unexpected result")
				}
				return
			}
			if result == nil || result.Success != tc.success || result.Message != tc.message {
				t.Fatalf("result: %+v", result)
			}
		})
	}
}

func TestRedirectCancellationAndRedaction(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	borrowed := source.Client()
	client := siteattendance.NewClient(borrowed)
	result, err := client.CheckinSite(context.Background(), source.URL, "fixture-cookie")
	if err != nil || result == nil || result.Success || result.Message != "站点返回状态码 302" || reached || borrowed.CheckRedirect != nil || borrowed.Timeout != 0 {
		t.Fatalf("redirect/client policy changed: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.CheckinSite(ctx, source.URL, "fixture-cookie"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	for _, address := range []string{"", ":bad/secret-cookie", "http://user:secret-cookie@localhost"} {
		if _, err := client.CheckinSite(context.Background(), address, "fixture-cookie"); err != ports.ErrRejected {
			t.Fatalf("invalid URL error: %v", err)
		}
	}
	source.Close()
	if _, err := client.CheckinSite(context.Background(), source.URL+"/secret-cookie", "fixture-cookie"); err != ports.ErrUnavailable {
		t.Fatalf("transport error: %v", err)
	}
	if _, err := siteattendance.NewClient(nil).CheckinSite(context.Background(), source.URL, ""); err != ports.ErrUnavailable {
		t.Fatalf("missing client: %v", err)
	}
}

func TestIncompleteBodyCannotConfirmAttendance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = fmt.Fprint(w, "签到成功")
	}))
	defer server.Close()
	if result, err := siteattendance.NewClient(server.Client()).CheckinSite(context.Background(), server.URL, ""); result != nil || err != ports.ErrBadResponse {
		t.Fatalf("partial body: %+v %v", result, err)
	}
}
