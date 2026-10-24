package app

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

func TestM11WebPushMovieIngestionPG(t *testing.T) {
	for _, scenario := range []string{"accepted", "expired", "limited"} {
		t.Run(scenario, func(t *testing.T) {
			runFullMoviePipelineConfigured(t, false, "", "", false, true, func(f *organizationFixture, _ *ports.Actor, _ uuid.UUID, a *App) func() {
				return m11WebPushMovieProbe(t, f, a, scenario)
			})
		})
	}
}
func m11WebPushMovieProbe(t *testing.T, f *organizationFixture, a *App, scenario string) func() {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.WebPush = config.WebPushConfig{Enabled: true, PublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), PrivateKey: base64.RawURLEncoding.EncodeToString(private.Bytes()), Subject: "mailto:push@example.org"}
	browser, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	codec := crypto.NewSecretProtector(outboxTestKey)
	ob := outbox.New(f.pool, f.gen, a.clock)
	repo := postgres.NewWebPushStore(f.pool, f.gen, ob)
	service := notify.NewWebPushService(repo, codec, a.cfg.WebPush.PublicKey, true)
	actor := notificationFixtureAdmin(t, f)
	cfg, err := service.Configuration(t.Context(), actor)
	if err != nil {
		t.Fatal(err)
	}
	sub := ports.WebPushSubscription{Endpoint: "https://fcm.googleapis.com/owned-good", P256DH: base64.RawURLEncoding.EncodeToString(browser.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{29}, 16))}
	good, err := service.Register(t.Context(), actor, uuid.New(), cfg.Version, sub, "push-good")
	if err != nil {
		t.Fatal(err)
	}
	secondary := sub
	secondary.Endpoint = "https://fcm.googleapis.com/owned-secondary"
	second, err := service.Register(t.Context(), actor, uuid.New(), cfg.Version, secondary, "push-secondary")
	if err != nil {
		t.Fatal(err)
	}
	var goodCalls, secondCalls atomic.Int32
	native := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil || r.Method != http.MethodPost || r.Header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") || r.Header.Get("TTL") != "300" || len(raw) < 100 || len(raw) > 4096 || bytes.Contains(raw, []byte("媒体已入库")) {
			t.Error("not native encrypted Web Push", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/owned-good":
			goodCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
		case "/owned-secondary":
			secondCalls.Add(1)
			switch scenario {
			case "expired":
				w.WriteHeader(http.StatusGone)
			case "limited":
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(http.StatusTooManyRequests)
			default:
				w.WriteHeader(http.StatusCreated)
			}
		default:
			t.Error("unbound push endpoint")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(native.Close)
	target, err := url.Parse(native.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := native.Client().Transport
	ownedWebhook := make(map[string]bool)
	channels, err := f.q.ListActiveNotificationChannels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range channels {
		raw, decodeErr := codec.DecryptSecret(channel.ConfigEncrypted)
		var config struct {
			URL string `json:"url"`
		}
		if decodeErr != nil || json.Unmarshal(raw, &config) != nil || config.URL == "" {
			t.Fatal("invalid owned notification fixture")
		}
		ownedWebhook[config.URL] = true
	}
	webhookTransport := &http.Transport{Proxy: nil}
	t.Cleanup(webhookTransport.CloseIdleConnections)
	a.notificationHTTPClient = &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "fcm.googleapis.com" {
			if ownedWebhook[r.URL.String()] {
				return webhookTransport.RoundTrip(r)
			}
			t.Error("unexpected outbound push host")
			return nil, ports.ErrRejected
		}
		cloned := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		cloned.URL = &u
		cloned.Host = target.Host
		return transport.RoundTrip(cloned)
	}), Timeout: 10 * time.Second}
	return func() {
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
		defer cancel()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		var goodState, secondState, secondStatus string
		var delay bool
		for {
			err = f.pool.QueryRow(ctx, `SELECT COALESCE((SELECT state FROM outbox_delivery WHERE consumer=$1 LIMIT 1),''),COALESCE((SELECT state FROM outbox_delivery WHERE consumer=$2 LIMIT 1),''),(SELECT status FROM web_push_device WHERE id=$3),COALESCE((SELECT not_before>clock_timestamp()+interval '60 seconds' FROM outbox_delivery WHERE consumer=$2 LIMIT 1),false)`, "webpush:"+good.ID.String(), "webpush:"+second.ID.String(), second.ID).Scan(&goodState, &secondState, &secondStatus, &delay)
			ready := goodState == "delivered" && secondState == "delivered"
			if scenario == "expired" {
				ready = goodState == "delivered" && secondState == "skipped" && secondStatus == "expired"
			}
			if scenario == "limited" {
				ready = goodState == "delivered" && secondState == "pending" && delay
			}
			if err == nil && ready {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("push delivery did not settle: %s %s %s rate=%v err=%v", goodState, secondState, secondStatus, delay, err)
			case <-ticker.C:
			}
		}
		if goodCalls.Load() != 1 || secondCalls.Load() != 1 {
			t.Fatal("notification replayed before authorized retry", goodCalls.Load(), secondCalls.Load())
		}
		before := m11ArtworkProtected(t, f)
		receipt, err := service.Receipt(t.Context(), actor, "push-good")
		if err != nil || receipt.ID != good.ID {
			t.Fatal("device receipt changed", err)
		}
		if before != m11ArtworkProtected(t, f) {
			t.Fatal("push receipt changed download or ingestion")
		}
		var bindings int
		if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM web_push_delivery_binding b JOIN outbox_delivery d ON d.id=b.delivery_id JOIN outbox_event e ON e.id=d.event_id WHERE e.event_type='media.imported'`).Scan(&bindings); err != nil || bindings != 2 {
			t.Fatal("recipient binding missing", bindings, err)
		}
		raw, _ := json.Marshal(receipt)
		if bytes.Contains(raw, []byte(sub.Endpoint)) || bytes.Contains(raw, []byte(sub.Auth)) {
			t.Fatal("private delivery capability in receipt")
		}
		t.Logf("M11_WEB_PUSH_MOVIE_APP_PASS scenario=%s App=PRODUCTION Add=ONCE import=REAL_HARDLINK native=ENCRYPTED_TLS recipients=FROZEN other_device=INDEPENDENT original_key=READ_ONLY", scenario)
	}
}
