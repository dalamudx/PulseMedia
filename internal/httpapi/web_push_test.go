package httpapi

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

type webPushHTTPRepo struct {
	calls  int
	fail   error
	device ports.WebPushDevice
}

func (r *webPushHTTPRepo) RegisterWebPush(_ context.Context, _ *ports.Actor, id uuid.UUID, _ string, _ string, p ports.WebPushRegistrationPrepare) (*ports.WebPushDevice, error) {
	r.calls++
	if r.fail != nil {
		return nil, r.fail
	}
	if _, err := p(); err != nil {
		return nil, err
	}
	r.device.ID = id
	return &r.device, nil
}
func (r *webPushHTTPRepo) RevokeWebPush(context.Context, *ports.Actor, uuid.UUID, time.Time, string, string) (*ports.WebPushDevice, error) {
	r.calls++
	return &r.device, r.fail
}
func (r *webPushHTTPRepo) ListWebPush(context.Context, *ports.Actor) ([]ports.WebPushDevice, error) {
	r.calls++
	if r.fail != nil {
		return nil, r.fail
	}
	return []ports.WebPushDevice{r.device}, nil
}
func (r *webPushHTTPRepo) GetWebPushReceipt(context.Context, *ports.Actor, string) (*ports.WebPushDevice, error) {
	r.calls++
	return &r.device, r.fail
}

type webPushHTTPCodec struct{}

func (webPushHTTPCodec) EncryptSecret(raw []byte) ([]byte, error) {
	return append(make([]byte, 40), raw...), nil
}
func (webPushHTTPCodec) DecryptSecret([]byte) ([]byte, error) { return nil, errors.New("not used") }
func (webPushHTTPCodec) DigestSecret([]byte) (string, error)  { return strings.Repeat("a", 64), nil }
func webPushHTTPFixture(t *testing.T) (http.Handler, *webPushHTTPRepo, *ports.Actor, string) {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	repo := &webPushHTTPRepo{device: ports.WebPushDevice{ID: uuid.New(), Status: "active", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}}
	service := notify.NewWebPushService(repo, webPushHTTPCodec{}, public, true)
	config, err := service.Configuration(t.Context(), actor)
	if err != nil {
		t.Fatal(err)
	}
	repo.calls = 0
	h := &ServerHandler{}
	h.SetWebPushService(service)
	raw, err := json.Marshal(map[string]any{"id": repo.device.ID, "vapid_version": config.Version, "subscription": map[string]string{"endpoint": "https://fcm.googleapis.com/owned-browser", "p256dh": public, "auth": base64.RawURLEncoding.EncodeToString(make([]byte, 16))}})
	if err != nil {
		t.Fatal(err)
	}
	return webPushNoStore(ProblemResponseMiddleware()(strictWebPushJSON(Handler(NewStrictHandler(h, nil))))), repo, actor, string(raw)
}
func webPushHTTPRequest(t *testing.T, h http.Handler, a *ports.Actor, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "owned-command")
	if a != nil {
		r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status/cache %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if strings.Contains(w.Body.String(), "fcm.googleapis.com") {
		t.Fatal("capability leaked")
	}
	return w
}
func TestWebPushHTTPAccountEntryAndSafeResponse(t *testing.T) {
	h, r, a, body := webPushHTTPFixture(t)
	webPushHTTPRequest(t, h, a, http.MethodGet, "/api/v1/web-push/config", "", http.StatusOK)
	webPushHTTPRequest(t, h, a, http.MethodPost, "/api/v1/web-push/devices", body, http.StatusCreated)
	w := webPushHTTPRequest(t, h, a, http.MethodGet, "/api/v1/web-push/devices", "", http.StatusOK)
	var rows []map[string]any
	if json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 || len(rows[0]) != 4 {
		t.Fatal("unsafe summary", w.Body.String())
	}
	webPushHTTPRequest(t, h, a, http.MethodGet, "/api/v1/web-push/receipts/owned-command", "", http.StatusOK)
	raw, _ := json.Marshal(map[string]any{"expected_updated_at": r.device.UpdatedAt})
	webPushHTTPRequest(t, h, a, http.MethodPut, "/api/v1/web-push/devices/"+r.device.ID.String()+"/revocation", string(raw), http.StatusOK)
	if r.calls != 5 {
		t.Fatal("unexpected repository calls", r.calls)
	}
}
func TestWebPushHTTPAuthorityBeforeBinding(t *testing.T) {
	h, r, a, _ := webPushHTTPFixture(t)
	for _, actor := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: a.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: a.ID, Role: ports.RoleMember, Scopes: []string{}}} {
		status := http.StatusForbidden
		if actor == nil {
			status = http.StatusUnauthorized
		}
		webPushHTTPRequest(t, h, actor, http.MethodPost, "/api/v1/web-push/devices", "{broken", status)
	}
	if r.calls != 0 {
		t.Fatal("invalid authority reached storage")
	}
}
func TestWebPushHTTPStrictJSONAndQuery(t *testing.T) {
	h, r, a, body := webPushHTTPFixture(t)
	for _, bad := range []string{body + body, strings.Replace(body, "\"id\":", "\"id\":\"00000000-0000-0000-0000-000000000000\",\"id\":", 1), strings.Replace(body, "\"auth\":", "\"Auth\":", 1), strings.Replace(body, "\"auth\":", "\"auth\":\"duplicate\",\"auth\":", 1), strings.Replace(body, "\"subscription\":", "\"Subscription\":", 1), strings.Replace(body, "\"endpoint\":", "\"extra\":true,\"endpoint\":", 1), `{"id":null}`, strings.Repeat(" ", 8193)} {
		webPushHTTPRequest(t, h, a, http.MethodPost, "/api/v1/web-push/devices", bad, http.StatusBadRequest)
	}
	for _, path := range []string{"/api/v1/web-push/devices?unknown=1", "/api/v1/web-push/config?", "/api/v1/web-push/receipts/key?x=1"} {
		webPushHTTPRequest(t, h, a, http.MethodGet, path, "", http.StatusBadRequest)
	}
	if r.calls != 0 {
		t.Fatal("invalid shape reached storage", r.calls)
	}
}
func TestWebPushHTTPErrorClassification(t *testing.T) {
	h, r, a, body := webPushHTTPFixture(t)
	for _, tc := range []struct {
		err    error
		status int
	}{{ports.ErrForbidden, http.StatusForbidden}, {ports.ErrWebPushConflict, http.StatusConflict}, {ports.ErrUnavailable, http.StatusServiceUnavailable}} {
		r.fail = tc.err
		webPushHTTPRequest(t, h, a, http.MethodPost, "/api/v1/web-push/devices", body, tc.status)
	}
	r.fail = ports.ErrNotFound
	webPushHTTPRequest(t, h, a, http.MethodGet, "/api/v1/web-push/receipts/missing", "", http.StatusNotFound)
	r.fail = ports.ErrUnavailable
	webPushHTTPRequest(t, h, a, http.MethodGet, "/api/v1/web-push/devices", "", http.StatusServiceUnavailable)
}
