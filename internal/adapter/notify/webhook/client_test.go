package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/notify/webhook"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestWebhookHMACAndReceiverDeduplication(t *testing.T) {
	secret := "topsecret_hmac_key_12345"
	receivedEvents := make([]string, 0)
	verifiedSignatures := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eventID := r.Header.Get("X-PulseMedia-Event-ID")
		sigHeader := r.Header.Get("X-PulseMedia-Signature")

		var n ports.Notification
		_ = json.NewDecoder(r.Body).Decode(&n)

		// Verify HMAC-SHA256 signature
		bodyBytes, _ := json.Marshal(n)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(bodyBytes)
		expectedSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		if sigHeader == expectedSig {
			verifiedSignatures++
		}
		receivedEvents = append(receivedEvents, eventID)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := webhook.NewClient(server.URL, secret, nil)
	ctx := context.Background()

	notif := ports.Notification{
		EventID:   "evt-unique-12345",
		Topic:     "media.imported",
		Title:     "电影入库成功",
		Body:      "Fight Club (1999) [1080p] 已成功入库",
		Severity:  "success",
		Timestamp: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}

	err := client.Send(ctx, notif)
	if err != nil {
		t.Fatalf("send webhook failed: %v", err)
	}

	if len(receivedEvents) != 1 || receivedEvents[0] != "evt-unique-12345" {
		t.Fatalf("expected event ID passed in header: %+v", receivedEvents)
	}
	if verifiedSignatures != 1 {
		t.Fatal("HMAC signature verification failed")
	}
}
