package cookiecloud_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/cookiecloud"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestCookieCloudSyncLifecycle(t *testing.T) {
	for _, mode := range []struct{ kind, cipher string }{{"legacy", legacyBackup}, {"aes-128-cbc-fixed", fixedBackup}} {
		t.Run(mode.kind, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/prefix/get/fixture-user" || r.URL.RawQuery != "" || r.ContentLength > 0 || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("GET exposed credentials or wrong URL")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"encrypted":%q,"crypto_type":%q}`, mode.cipher, mode.kind)
			}))
			defer server.Close()
			client := cookiecloud.NewClient(server.Client())
			backup, err := client.ReadBackup(t.Context(), server.URL+"/prefix", "fixture-user", "fixture-password")
			if err != nil || len(backup.Cookies) != 1 || len(backup.Cookies["tracker.example"]) != 1 || backup.Cookies["tracker.example"][0].Value != "owned-fixture" {
				t.Fatal("encrypted native backup failed", err)
			}
			// Missing native domain/path/hostOnly facts remain unknown; no scope invented.
			item := backup.Cookies["tracker.example"][0]
			if item.Domain != "" || item.Path != "" || item.HostOnly != nil || item.Secure != nil {
				t.Fatal("missing native scope was fabricated")
			}
			if strings.Contains(fmt.Sprint(backup), "owned-fixture") || strings.Contains(fmt.Sprint(item), "owned-fixture") {
				t.Fatal("formatting leaked credentials")
			}
			cookies, err := client.SyncCookies(t.Context(), server.URL+"/prefix", "fixture-user", "fixture-password")
			if err != nil || cookies["tracker.example"] != "session=owned-fixture" || calls.Load() != 2 {
				t.Fatal("compatibility view changed native reads", err)
			}
		})
	}
}

func TestCookieCloudEncryptedOnlyAndStatus(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"plaintext", backupPlain, 200, ports.ErrBadResponse},
		{"duplicate", `{"encrypted":"x","encrypted":"y"}`, 200, ports.ErrBadResponse},
		{"wrong-password", `{"encrypted":"` + legacyBackup + `"}`, 200, ports.ErrBadResponse},
		{"future", `{"encrypted":"` + legacyBackup + `","crypto_type":"future"}`, 200, ports.ErrBadResponse},
		{"auth", "private-server-details", 401, ports.ErrAuthRequired},
		{"forbidden", "private-server-details", 403, ports.ErrAuthRequired},
		{"missing", "private-server-details", 404, ports.ErrNotFound},
		{"unavailable", "private-server-details", 503, ports.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			result, err := cookiecloud.NewClient(server.Client()).ReadBackup(t.Context(), server.URL, "fixture-user", "wrong")
			if result != nil || !errors.Is(err, tc.want) || calls.Load() != 1 {
				t.Fatal("encrypted error contract mismatch", err)
			}
			if strings.Contains(err.Error(), "private-server-details") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "fixture-user") {
				t.Fatal("error leaked upstream identity or body")
			}
		})
	}
}

func TestCookieCloudNoRedirectRetryAndCancellation(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	_, err := cookiecloud.NewClient(source.Client()).ReadBackup(t.Context(), source.URL, "fixture-user", "fixture-password")
	if !errors.Is(err, ports.ErrBadResponse) || calls.Load() != 0 {
		t.Fatal("backup followed redirect", err)
	}
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}))
	defer limited.Close()
	_, err = cookiecloud.NewClient(limited.Client()).ReadBackup(t.Context(), limited.URL, "fixture-user", "fixture-password")
	wait, ok := ports.IsRateLimited(err)
	if !ok || wait.RetryAfter != time.Minute || calls.Load() != 1 {
		t.Fatal("rate limit retried or lost retry advice")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = cookiecloud.NewClient(limited.Client()).ReadBackup(ctx, limited.URL, "fixture-user", "fixture-password")
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatal("cancelled read sent a request")
	}
}

func TestCookieCloudRejectsUnsafeConfigurationBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := cookiecloud.NewClient(server.Client())
	for _, tc := range []struct{ name, url, key, password string }{
		{"relative", "/relative", "fixture-user", "fixture-password"},
		{"scheme", "ftp://tracker.example", "fixture-user", "fixture-password"},
		{"userinfo", strings.Replace(server.URL, "://", "://private:secret@", 1), "fixture-user", "fixture-password"},
		{"query", server.URL + "?password=private", "fixture-user", "fixture-password"},
		{"fragment", server.URL + "#private", "fixture-user", "fixture-password"},
		{"escaped-path", server.URL + "/%2fprivate", "fixture-user", "fixture-password"},
		{"empty-key", server.URL, "", "fixture-password"},
		{"path-key", server.URL, "../private", "fixture-password"},
		{"slash-key", server.URL, "a/b", "fixture-password"},
		{"query-key", server.URL, "a?private", "fixture-password"},
		{"unicode-key", server.URL, "媒体", "fixture-password"},
		{"long-key", server.URL, strings.Repeat("a", 129), "fixture-password"},
		{"empty-password", server.URL, "fixture-user", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backup, err := client.ReadBackup(t.Context(), tc.url, tc.key, tc.password)
			if backup != nil || !errors.Is(err, ports.ErrRejected) || calls.Load() != 0 {
				t.Fatal("unsafe configuration reached the network or wrong classification", err)
			}
		})
	}
}
