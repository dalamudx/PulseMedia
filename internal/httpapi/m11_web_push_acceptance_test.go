package httpapi_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/google/uuid"
)

func pushAcceptanceConfig(t *testing.T) config.WebPushConfig {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return config.WebPushConfig{Enabled: true, PublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), PrivateKey: base64.RawURLEncoding.EncodeToString(private.Bytes()), Subject: "mailto:push@example.org"}
}
func pushAcceptanceStatus(t *testing.T, r m5IndexerHTTPResult, status int) {
	t.Helper()
	m5IndexerStatus(t, r, status)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("push response may be cached")
	}
	for _, secret := range []string{"fcm.googleapis.com", "SQLSTATE", "owned_push_tail_private", "subscription_encrypted"} {
		if bytes.Contains(r.body, []byte(secret)) {
			t.Fatal("push response leaked capabilities or storage data")
		}
	}
}
func pushAcceptanceDevice(t *testing.T, r m5IndexerHTTPResult, status int) httpapi.WebPushDevice {
	t.Helper()
	pushAcceptanceStatus(t, r, status)
	var d httpapi.WebPushDevice
	if json.Unmarshal(r.body, &d) != nil || d.Id == uuid.Nil || d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() {
		t.Fatal("invalid device summary")
	}
	return d
}
func TestM11WebPushRegistrationCookiePG(t *testing.T) {
	cfg := pushAcceptanceConfig(t)
	f, metadata := newM5CreationAcceptance(t, func(c *config.Config) { c.WebPush = cfg })
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'push-member',password_hash,'member',true FROM app_user WHERE username='creation-admin'`, uuid.New())
	member := m5CreationLogin(t, f, "push-member")
	m9WordHTTPStable(t, f)
	base := m9MediaRequestState(t, f)
	calls := metadata.count()
	r := f.request(t, http.MethodGet, "/api/v1/web-push/config", "", nil, member)
	pushAcceptanceStatus(t, r, http.StatusOK)
	var public httpapi.WebPushConfiguration
	if json.Unmarshal(r.body, &public) != nil || !public.Enabled || public.PublicKey != cfg.PublicKey || strings.Contains(string(r.body), cfg.PrivateKey) {
		t.Fatal("unsafe deployment response")
	}
	m9MediaRequestUnchanged(t, f, base)
	browser := pushAcceptanceConfig(t)
	id := uuid.New()
	input := map[string]any{"id": id, "vapid_version": public.Version, "subscription": map[string]string{"endpoint": "https://fcm.googleapis.com/browser-acceptance", "p256dh": browser.PublicKey, "auth": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{19}, 16))}}
	saved := pushAcceptanceDevice(t, f.request(t, http.MethodPost, "/api/v1/web-push/devices", "push-register", input, member), http.StatusCreated)
	after := m9MediaRequestState(t, f)
	for _, table := range []string{"web_push_device", "audit_log", "outbox_event", "idempotency_record"} {
		delete(base, table)
		delete(after, table)
	}
	if !reflect.DeepEqual(base, after) {
		t.Fatal("registration changed protected business tables")
	}
	baseline := m9MediaRequestState(t, f)
	restored := pushAcceptanceDevice(t, f.request(t, http.MethodPost, "/api/v1/web-push/devices", "push-register", input, member), http.StatusCreated)
	if !reflect.DeepEqual(saved, restored) {
		t.Fatal("original key changed")
	}
	pushAcceptanceDevice(t, f.request(t, http.MethodGet, "/api/v1/web-push/receipts/push-register", "", nil, member), http.StatusOK)
	pushAcceptanceStatus(t, f.request(t, http.MethodGet, "/api/v1/web-push/receipts/push-register", "", nil, f.session), http.StatusNotFound)
	pushAcceptanceStatus(t, f.request(t, http.MethodGet, "/api/v1/web-push/devices", "", nil, f.session), http.StatusOK)
	m9MediaRequestUnchanged(t, f, baseline)
	revoke := map[string]any{"expected_updated_at": saved.UpdatedAt}
	path := "/api/v1/web-push/devices/" + id.String() + "/revocation"
	pushAcceptanceStatus(t, f.request(t, http.MethodPut, path, "foreign-revoke", revoke, f.session), http.StatusNotFound)
	m9MediaRequestUnchanged(t, f, baseline)
	removed := pushAcceptanceDevice(t, f.request(t, http.MethodPut, path, "push-revoke", revoke, member), http.StatusOK)
	if removed.Status != "revoked" {
		t.Fatal("revoke not persisted")
	}
	baseline = m9MediaRequestState(t, f)
	if old := pushAcceptanceDevice(t, f.request(t, http.MethodGet, "/api/v1/web-push/receipts/push-register", "", nil, member), http.StatusOK); old.Status != "active" {
		t.Fatal("historical receipt rewritten")
	}
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, "UPDATE app_user SET is_active=false WHERE username='push-member'")
	pushAcceptanceStatus(t, f.request(t, http.MethodGet, "/api/v1/web-push/devices", "", nil, member), http.StatusUnauthorized)
	if metadata.count() != calls {
		t.Fatal("device configuration called metadata")
	}
	t.Log("M11_PUSH_DEVICE_COOKIE_PASS App=PRODUCTION Cookie=REAL actor=MEMBER ownership=EXACT original_key=READ_ONLY registration=ZERO_DELIVERY credentials=PRIVATE tables=PROTECTED")
}
func TestM11WebPushRegistrationRollbackCookiePG(t *testing.T) {
	cfg := pushAcceptanceConfig(t)
	f, metadata := newM5CreationAcceptance(t, func(c *config.Config) { c.WebPush = cfg })
	m9WordHTTPStable(t, f)
	calls := metadata.count()
	r := f.request(t, http.MethodGet, "/api/v1/web-push/config", "", nil, f.session)
	pushAcceptanceStatus(t, r, http.StatusOK)
	var public httpapi.WebPushConfiguration
	if json.Unmarshal(r.body, &public) != nil {
		t.Fatal("configuration decode")
	}
	input := map[string]any{"id": uuid.New(), "vapid_version": public.Version, "subscription": map[string]string{"endpoint": "https://fcm.googleapis.com/browser-rollback", "p256dh": cfg.PublicKey, "auth": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{21}, 16))}}
	m5InteractiveSQL(t, f, `CREATE FUNCTION push_cookie_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned_push_tail_private'; END $$; CREATE TRIGGER push_cookie_fault BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION push_cookie_fault()`)
	baseline := m9MediaRequestState(t, f)
	pushAcceptanceStatus(t, f.request(t, http.MethodPost, "/api/v1/web-push/devices", "push-tail", input, f.session), http.StatusServiceUnavailable)
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, `DROP TRIGGER push_cookie_fault ON idempotency_record; DROP FUNCTION push_cookie_fault()`)
	saved := pushAcceptanceDevice(t, f.request(t, http.MethodPost, "/api/v1/web-push/devices", "push-tail", input, f.session), http.StatusCreated)
	m5InteractiveSQL(t, f, `ALTER TABLE web_push_device RENAME TO web_push_device_owned_unavailable`)
	pushAcceptanceStatus(t, f.request(t, http.MethodGet, "/api/v1/web-push/devices", "", nil, f.session), http.StatusServiceUnavailable)
	receipt := pushAcceptanceDevice(t, f.request(t, http.MethodGet, "/api/v1/web-push/receipts/push-tail", "", nil, f.session), http.StatusOK)
	if receipt.Id != saved.Id {
		t.Fatal("receipt depended on live device reads")
	}
	m5InteractiveSQL(t, f, `ALTER TABLE web_push_device_owned_unavailable RENAME TO web_push_device`)
	if metadata.count() != calls {
		t.Fatal("registration contacted metadata")
	}
	t.Log("M11_PUSH_DEVICE_ROLLBACK_PASS tail=FULL_ROLLBACK original_key=REUSABLE unavailable=503 receipt=READ_ONLY external=ZERO")
}
