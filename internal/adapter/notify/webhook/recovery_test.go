package webhook_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/notify/webhook"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookRejectsRedirectAndKeepsRateLimitContract(t *testing.T) {
	var leaked atomic.Int32
	recipient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.WriteHeader(200) }))
	defer recipient.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, recipient.URL, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	}))
	defer server.Close()
	supplied := &http.Client{Timeout: time.Second}
	client := webhook.NewClient(server.URL+"/redirect", "private-signing-secret", supplied)
	if err := client.Send(context.Background(), ports.Notification{EventID: "event"}); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal(err)
	}
	if leaked.Load() != 0 || supplied.CheckRedirect != nil {
		t.Fatal("forwarded signature or mutated caller-owned http client")
	}
	client = webhook.NewClient(server.URL+"/rate", "private-signing-secret", nil)
	var rate *ports.RateLimitError
	if err := client.Send(context.Background(), ports.Notification{EventID: "event"}); !errors.As(err, &rate) || rate.RetryAfter != 2*time.Minute {
		t.Fatal("lost Retry-After", err)
	}
}
