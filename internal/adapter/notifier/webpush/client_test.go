package webpush_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/notifier/webpush"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var instant = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func material(t *testing.T) (webpush.Config, webpush.Subscription, *ecdh.PrivateKey) {
	t.Helper()
	application, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return webpush.Config{PublicKey: encode(application.PublicKey().Bytes()), PrivateKey: encode(application.Bytes()), Subject: "mailto:push@example.org"}, webpush.Subscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/opaque-owned-endpoint",
		P256DH:   encode(receiver.PublicKey().Bytes()), Auth: encode(bytes.Repeat([]byte{7}, 16)),
	}, receiver
}

func client(t *testing.T, config webpush.Config, transport http.RoundTripper) *webpush.Client {
	t.Helper()
	c, err := webpush.NewClient(config, &http.Client{Transport: transport}, func() time.Time { return instant })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tlsTransport(t *testing.T, server *httptest.Server) http.RoundTripper {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := server.Client().Transport
	return roundTrip(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return base.RoundTrip(clone)
	})
}

func TestWebPushEncryptedVAPIDTLS(t *testing.T) {
	config, subscription, receiver := material(t)
	type receipt struct {
		header http.Header
		body   []byte
		err    error
	}
	received := make(chan receipt, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if r.Method != http.MethodPost || r.URL.Path != "/fcm/send/opaque-owned-endpoint" {
			t.Error("wrong native request")
		}
		received <- receipt{r.Header.Clone(), body, err}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	c := client(t, config, tlsTransport(t, server))
	payload := webpush.NotificationPayload{Title: "影视已入库", Body: "独立浏览器解密验证", URL: "/activity", Icon: "/icon.png", Tag: "event-owned"}
	var prior []byte
	for attempt := range 2 {
		if attempt == 1 {
			subscription.Endpoint = "https://fcm.googleapis.com:443/fcm/send/opaque-owned-endpoint"
		}
		if err := c.SendNotification(t.Context(), subscription, payload); err != nil {
			t.Fatal(err)
		}
		result := <-received
		if result.err != nil {
			t.Fatal(result.err)
		}
		if len(result.body) != 4096 || bytes.Contains(result.body, []byte(payload.Body)) || bytes.Equal(result.body, prior) {
			t.Fatal("payload not padded, encrypted or independently randomized")
		}
		if result.header.Get("Content-Encoding") != "aes128gcm" || result.header.Get("Content-Type") != "application/octet-stream" || result.header.Get("TTL") != "300" {
			t.Fatal("bad native headers")
		}
		verifyVAPID(t, result.header.Get("Authorization"), config)
		plaintext := decrypt(t, result.body, receiver, subscription.Auth)
		var got webpush.NotificationPayload
		if err := json.Unmarshal(plaintext, &got); err != nil || got != payload {
			t.Fatal("browser plaintext mismatch")
		}
		prior = result.body
	}
}

func verifyVAPID(t *testing.T, authorization string, config webpush.Config) {
	t.Helper()
	if !strings.HasPrefix(authorization, "vapid t=") {
		t.Fatal("missing VAPID")
	}
	fields := strings.Split(strings.TrimPrefix(authorization, "vapid t="), ", k=")
	if len(fields) != 2 || fields[1] != config.PublicKey {
		t.Fatal("wrong application identity")
	}
	parts := strings.Split(fields[0], ".")
	if len(parts) != 3 {
		t.Fatal("invalid JWS")
	}
	decode := func(value string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var header struct {
		Algorithm string `json:"alg"`
	}
	var claims struct {
		Audience string `json:"aud"`
		Subject  string `json:"sub"`
		Expires  int64  `json:"exp"`
	}
	if json.Unmarshal(decode(parts[0]), &header) != nil || header.Algorithm != "ES256" || json.Unmarshal(decode(parts[1]), &claims) != nil {
		t.Fatal("invalid signed claims")
	}
	if claims.Audience != "https://fcm.googleapis.com" || claims.Subject != config.Subject || claims.Expires != instant.Add(time.Hour).Unix() {
		t.Fatal("incorrect audience, contact or injected expiry")
	}
	public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), decode(config.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	signature := decode(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(signature) != 64 || !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Fatal("invalid VAPID signature")
	}
}

// This receiver does not call the sending library: it derives the RFC8291 keys
// independently, then opens the RFC8188 record with the browser private key.
func decrypt(t *testing.T, record []byte, receiver *ecdh.PrivateKey, auth string) []byte {
	t.Helper()
	if len(record) < 102 || binary.BigEndian.Uint32(record[16:20]) != 4096 || record[20] != 65 {
		t.Fatal("invalid encrypted record")
	}
	sender, err := ecdh.P256().NewPublicKey(record[21:86])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := receiver.ECDH(sender)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base64.RawURLEncoding.DecodeString(auth)
	if err != nil {
		t.Fatal(err)
	}
	mac := func(key, value []byte) []byte {
		h := hmac.New(sha256.New, key)
		_, _ = h.Write(value)
		return h.Sum(nil)
	}
	expand := func(key, info []byte, size int) []byte {
		input := append(bytes.Clone(info), 1)
		return mac(key, input)[:size]
	}
	info := append([]byte("WebPush: info\x00"), receiver.PublicKey().Bytes()...)
	info = append(info, sender.Bytes()...)
	ikm := expand(mac(secret, shared), info, 32)
	prk := mac(record[:16], ikm)
	block, err := aes.NewCipher(expand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	clear, err := gcm.Open(nil, expand(prk, []byte("Content-Encoding: nonce\x00"), 12), record[86:], nil)
	if err != nil {
		t.Fatal(err)
	}
	clear = bytes.TrimRight(clear, "\x00")
	if len(clear) == 0 || clear[len(clear)-1] != 2 {
		t.Fatal("invalid final record delimiter")
	}
	return clear[:len(clear)-1]
}

func TestWebPushSubscriptionBoundaries(t *testing.T) {
	_, valid, _ := material(t)
	for _, host := range []string{"fcm.googleapis.com", "updates.push.services.mozilla.com", "web.push.apple.com", "wns2-owned.notify.windows.com"} {
		value := valid
		value.Endpoint = "https://" + host + "/opaque"
		if err := webpush.ValidateSubscription(value); err != nil {
			t.Fatal(err)
		}
	}
	bad := map[string]func(*webpush.Subscription){
		"http":       func(s *webpush.Subscription) { s.Endpoint = "http://fcm.googleapis.com/push" },
		"private":    func(s *webpush.Subscription) { s.Endpoint = "https://127.0.0.1/push" },
		"foreign":    func(s *webpush.Subscription) { s.Endpoint = "https://fcm.googleapis.com.attacker.test/push" },
		"userinfo":   func(s *webpush.Subscription) { s.Endpoint = "https://user:password@fcm.googleapis.com/push" },
		"port":       func(s *webpush.Subscription) { s.Endpoint = "https://fcm.googleapis.com:444/push" },
		"fragment":   func(s *webpush.Subscription) { s.Endpoint += "#private" },
		"empty_path": func(s *webpush.Subscription) { s.Endpoint = "https://fcm.googleapis.com" },
		"oversized":  func(s *webpush.Subscription) { s.Endpoint += strings.Repeat("x", 4096) },
		"curve":      func(s *webpush.Subscription) { s.P256DH = base64.RawURLEncoding.EncodeToString(make([]byte, 65)) },
		"short_auth": func(s *webpush.Subscription) { s.Auth = "AA" },
		"padded_key": func(s *webpush.Subscription) { s.P256DH += "=" },
	}
	for name, change := range bad {
		t.Run(name, func(t *testing.T) {
			value := valid
			change(&value)
			if !errors.Is(webpush.ValidateSubscription(value), ports.ErrRejected) {
				t.Fatal("unsafe subscription accepted")
			}
		})
	}
}

func TestWebPushConfigurationAndPayloadBeforeIO(t *testing.T) {
	config, subscription, _ := material(t)
	second, _, _ := material(t)
	for name, invalid := range map[string]webpush.Config{
		"mismatched":  {PublicKey: second.PublicKey, PrivateKey: config.PrivateKey, Subject: config.Subject},
		"zero_scalar": {PublicKey: config.PublicKey, PrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Subject: config.Subject},
		"contact":     {PublicKey: config.PublicKey, PrivateKey: config.PrivateKey, Subject: "http://example.org"},
		"malformed":   {PublicKey: config.PublicKey, PrivateKey: "invalid", Subject: config.Subject},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := webpush.NewClient(invalid, nil, func() time.Time { return instant }); !errors.Is(err, ports.ErrRejected) {
				t.Fatal("invalid config accepted")
			}
		})
	}
	if _, err := webpush.NewClient(config, nil, nil); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("missing clock accepted")
	}
	var calls atomic.Int32
	c := client(t, config, roundTrip(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("must not send") }))
	for _, payload := range []webpush.NotificationPayload{{}, {Title: strings.Repeat("x", 161)}, {Title: "x", Body: strings.Repeat("x", 1801)}, {Title: "x", URL: "https://attacker.test"}, {Title: "x", URL: "//attacker.test"}, {Title: "x", URL: "/%2Fattacker.test"}, {Title: "x", Icon: "/\\attacker.test"}, {Title: "x", Body: string([]byte{0xff})}, {Title: "x", Tag: strings.Repeat("x", 129)}} {
		if !errors.Is(c.SendNotification(t.Context(), subscription, payload), ports.ErrRejected) {
			t.Fatal("invalid payload accepted")
		}
	}
	invalid := subscription
	invalid.Auth = "bad"
	if !errors.Is(c.SendNotification(t.Context(), invalid, webpush.NotificationPayload{Title: "x"}), ports.ErrRejected) {
		t.Fatal("invalid browser key accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("validation caused network I/O")
	}
}

func TestWebPushHTTPFailuresAndBackoff(t *testing.T) {
	config, subscription, _ := material(t)
	for _, row := range []struct {
		name   string
		status int
		retry  string
		want   error
		delay  time.Duration
	}{
		{"expired", 410, "", ports.ErrNotFound, 0}, {"missing", 404, "", ports.ErrNotFound, 0}, {"auth", 401, "", ports.ErrAuthRequired, 0}, {"denied", 403, "", ports.ErrAuthRequired, 0},
		{"bad", 400, "", ports.ErrRejected, 0}, {"oversized", 413, "", ports.ErrRejected, 0}, {"server", 503, "", ports.ErrUnavailable, 0}, {"unexpected_success", 200, "", ports.ErrBadResponse, 0},
		{"seconds", 429, "73", nil, 73 * time.Second}, {"date", 429, instant.Add(time.Minute).Format(http.TimeFormat), nil, time.Minute}, {"huge", 429, "2147483647", nil, 24 * time.Hour}, {"invalid", 429, "-1", nil, 30 * time.Second},
	} {
		t.Run(row.name, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Retry-After", row.retry)
				w.WriteHeader(row.status)
				_, _ = w.Write([]byte("private-upstream-body"))
			}))
			defer server.Close()
			c := client(t, config, tlsTransport(t, server))
			err := c.SendNotification(t.Context(), subscription, webpush.NotificationPayload{Title: "x"})
			if row.delay > 0 {
				rate, ok := ports.IsRateLimited(err)
				if !ok || rate.RetryAfter != row.delay {
					t.Fatal("wrong persisted backoff")
				}
			} else if !errors.Is(err, row.want) {
				t.Fatalf("wrong failure class: %v", err)
			}
			if count.Load() != 1 || strings.Contains(err.Error(), "private-upstream-body") || strings.Contains(err.Error(), subscription.Endpoint) {
				t.Fatal("retry or unsafe error")
			}
		})
	}
}

func TestWebPushRedirectAndLostReceiptNeverReplay(t *testing.T) {
	config, subscription, _ := material(t)
	var count atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Location", "https://fcm.googleapis.com/other")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c := client(t, config, tlsTransport(t, server))
	if err := c.SendNotification(t.Context(), subscription, webpush.NotificationPayload{Title: "x"}); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("redirect accepted")
	}
	if count.Load() != 1 {
		t.Fatal("redirect followed")
	}
	count.Store(0)
	c = client(t, config, roundTrip(func(*http.Request) (*http.Response, error) {
		count.Add(1)
		return nil, errors.New("sensitive-endpoint-and-private-key")
	}))
	err := c.SendNotification(t.Context(), subscription, webpush.NotificationPayload{Title: "x"})
	if !errors.Is(err, ports.ErrUnavailable) || count.Load() != 1 || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("lost response retried or disclosed")
	}
}

func TestWebPushCancellationWaitsForPhysicalIO(t *testing.T) {
	config, subscription, _ := material(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	c := client(t, config, roundTrip(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return nil, r.Context().Err()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- c.SendNotification(ctx, subscription, webpush.NotificationPayload{Title: "x"}) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-result:
		t.Fatal("logical cancellation released executing I/O")
	case <-time.After(20 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not return")
	}
}
