package cookiecloud_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/cookiecloud"
)

func TestCookieCloudSyncLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get/user-123" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{
				"cookie_data": {
					"kp.m-team.cc": [
						{"name": "tp", "value": "token_mteam_xyz"},
						{"name": "cf_clearance", "value": "cf_val_123"}
					],
					"hdsky.me": [
						{"name": "passkey", "value": "pk_hdsky_456"}
					]
				}
			}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := cookiecloud.NewClient(server.Client())
	ctx := context.Background()

	// 1. Sync cookies from mock CookieCloud server
	cookies, err := client.SyncCookies(ctx, server.URL, "user-123", "password123")
	if err != nil {
		t.Fatalf("sync cookies failed: %v", err)
	}

	if len(cookies) != 2 {
		t.Fatalf("expected 2 domains, got: %d", len(cookies))
	}

	mteamCookie := cookies["kp.m-team.cc"]
	if mteamCookie != "tp=token_mteam_xyz; cf_clearance=cf_val_123" {
		t.Fatalf("unexpected mteam cookie: %s", mteamCookie)
	}

	hdskyCookie := cookies["hdsky.me"]
	if hdskyCookie != "passkey=pk_hdsky_456" {
		t.Fatalf("unexpected hdsky cookie: %s", hdskyCookie)
	}
}
