package app_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/notifier/webpush"
)

func TestM11WebPushNativeCompatibility(t *testing.T) { m11WebPushCompatibility(t, t.Context()) }

func m11WebPushCompatibility(t *testing.T, ctx context.Context) {
	t.Helper()
	application, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 4097))
		if readErr != nil || len(body) != 4096 || r.Method != http.MethodPost || r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("Authorization") == "" || bytes.Contains(body, []byte("入库完成")) {
			t.Error("invalid native encrypted push")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		count.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	native, err := webpush.NewClient(webpush.Config{PublicKey: encode(application.PublicKey().Bytes()), PrivateKey: encode(application.Bytes()), Subject: "mailto:push@example.org"}, &http.Client{Transport: m11PushCompatibilityTransport{base: server.Client().Transport, target: target}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	err = native.SendNotification(ctx, webpush.Subscription{Endpoint: "https://fcm.googleapis.com/fcm/send/owned", P256DH: encode(browser.PublicKey().Bytes()), Auth: encode(bytes.Repeat([]byte{3}, 16))}, webpush.NotificationPayload{Title: "入库完成", Body: "电影已成功入库", URL: "/activity"})
	if err != nil || count.Load() != 1 {
		t.Fatalf("native encrypted push failed: %v calls=%d", err, count.Load())
	}
}

type m11PushCompatibilityTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t m11PushCompatibilityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return t.base.RoundTrip(clone)
}
