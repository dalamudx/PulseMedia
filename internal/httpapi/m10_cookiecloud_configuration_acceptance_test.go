package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/google/uuid"
)

const m10CookieConfigPath = "/api/v1/integrations/cookiecloud/config"

func m10CookieConfigStatus(t *testing.T, result m5IndexerHTTPResult, status int) {
	t.Helper()
	m5IndexerStatus(t, result, status)
	if result.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("CookieCloud response is cacheable", status)
	}
	for _, secret := range []string{"owned-cloud-password", "owned-cloud-key", "private-cloud-prefix", "owned-cloud-store-fault"} {
		if bytes.Contains(result.body, []byte(secret)) {
			t.Fatal("CookieCloud response exposed a connection secret")
		}
	}
}
func m10CookieConfigInput(server string) map[string]any {
	return map[string]any{"expected_version": 0, "enabled": true, "interval_seconds": 1200, "connection": map[string]any{"server_url": server + "/private-cloud-prefix", "user_key": "owned-cloud-key", "password": "owned-cloud-password"}}
}
func m10CookieConfigOnlyChanges(t *testing.T, before, after map[string][32]byte) {
	t.Helper()
	allowed := map[string]bool{"cookiecloud_configuration": true, "audit_log": true, "outbox_event": true, "idempotency_record": true}
	for table, hash := range before {
		if !allowed[table] && after[table] != hash {
			t.Fatal("configuration save changed unrelated table", table)
		}
	}
	if len(before) != len(after) {
		t.Fatal("configuration save changed table inventory")
	}
}

func TestM10CookieCloudConfigCookieHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	admin := m5CreationLogin(t, f, "creation-admin")
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	for _, entry := range []struct{ name, role string }{{"cloud-other-admin", "admin"}, {"cloud-member", "member"}} {
		m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,$2,password_hash,$3,true FROM app_user WHERE username='creation-admin'`, uuid.New(), entry.name, entry.role)
	}
	other := m5CreationLogin(t, f, "cloud-other-admin")
	member := m5CreationLogin(t, f, "cloud-member")
	call := func(method, path, key string, body any, session string, status int) m5IndexerHTTPResult {
		t.Helper()
		result := f.request(t, method, path, key, body, session)
		m10CookieConfigStatus(t, result, status)
		return result
	}
	before := m9WordHTTPStable(t, f)
	metadataBefore, nativeBefore := metadata.count(), f.calls.Load()
	initial := call(http.MethodGet, m10CookieConfigPath, "", nil, admin, http.StatusOK)
	if strings.TrimSpace(string(initial.body)) != `{"config":null}` {
		t.Fatal("missing cloud configuration was not explicit null")
	}
	body := m10CookieConfigInput(remote.URL)
	saved := call(http.MethodPut, m10CookieConfigPath, "cloud-config-original", body, admin, http.StatusOK)
	var receipt httpapi.CookieCloudConfigReceipt
	if err := json.Unmarshal(saved.body, &receipt); err != nil || receipt.Config.Version != 1 || !receipt.Config.HasConnection || receipt.Config.ServerOrigin != remote.URL {
		t.Fatal("public configuration lost safe persisted facts", err)
	}
	after := m9WordHTTPStable(t, f)
	m10CookieConfigOnlyChanges(t, before, after)
	original := call(http.MethodGet, m10CookieConfigPath+"/commands/cloud-config-original", "", nil, admin, http.StatusOK)
	replay := call(http.MethodPut, m10CookieConfigPath, "cloud-config-original", body, admin, http.StatusOK)
	if !bytes.Equal(saved.body, original.body) || !bytes.Equal(saved.body, replay.body) {
		t.Fatal("original receipt is not byte-identical")
	}
	call(http.MethodGet, m10CookieConfigPath, "", nil, admin, http.StatusOK)
	otherRead := call(http.MethodGet, m10CookieConfigPath, "", nil, other, http.StatusOK)
	if strings.TrimSpace(string(otherRead.body)) != `{"config":null}` {
		t.Fatal("another administrator read the owner's connection")
	}
	call(http.MethodGet, m10CookieConfigPath+"/commands/cloud-config-original", "", nil, other, http.StatusNotFound)
	for _, bad := range []any{
		`{"expected_version":1,"enabled":true,"enabled":false,"interval_seconds":1200}`,
		`{"expected_version":1,"enabled":true,"interval_seconds":1200,"connection":{"server_url":"https://cloud.example","user_key":"key","password":"one","password":"two"}}`,
		`{"expected_version":1,"enabled":true,"interval_seconds":1200,"connection":null}`,
		`{"expected_version":1,"Enabled":true,"interval_seconds":1200}`,
	} {
		call(http.MethodPut, m10CookieConfigPath, "cloud-config-invalid", bad, admin, http.StatusBadRequest)
	}
	call(http.MethodGet, m10CookieConfigPath+"?private=yes", "", nil, admin, http.StatusBadRequest)
	call(http.MethodGet, m10CookieConfigPath, "", nil, "", http.StatusUnauthorized)
	call(http.MethodGet, m10CookieConfigPath, "", nil, member, http.StatusForbidden)
	call(http.MethodPut, m10CookieConfigPath, "cloud-member", body, member, http.StatusForbidden)
	call(http.MethodPut, m10CookieConfigPath, "cloud-stale", body, admin, http.StatusConflict)
	if !reflect.DeepEqual(after, m9WordHTTPState(t, f)) {
		t.Fatal("reads, replays or rejected commands changed full non-auth state")
	}
	var encrypted []byte
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT connection_encrypted FROM cookiecloud_configuration WHERE owner_id=$1`, receipt.Config.OwnerId).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPut, m10CookieConfigPath, "cloud-disable", map[string]any{"expected_version": 1, "enabled": false, "interval_seconds": 1200}, admin, http.StatusOK)
	var kept []byte
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT connection_encrypted FROM cookiecloud_configuration WHERE owner_id=$1`, receipt.Config.OwnerId).Scan(&kept); err != nil || !bytes.Equal(encrypted, kept) {
		t.Fatal("offline deactivation replaced encrypted connection", err)
	}
	m9WordHTTPStable(t, f)
	original = call(http.MethodGet, m10CookieConfigPath+"/commands/cloud-config-original", "", nil, admin, http.StatusOK)
	if !bytes.Equal(original.body, saved.body) {
		t.Fatal("deactivation changed historical receipt")
	}
	issued := f.request(t, http.MethodPost, "/api/v1/auth/tokens", "cloud-token", map[string]string{"name": "cloud-cookie-only"}, admin)
	m5IndexerStatus(t, issued, http.StatusOK)
	var token struct {
		Secret string `json:"secret"`
	}
	if json.Unmarshal(issued.body, &token) != nil || token.Secret == "" {
		t.Fatal("real token issuance failed")
	}
	for _, scopes := range []string{`["read","write"]`, `[]`, `["read","write","admin","system","*"]`} {
		m5InteractiveSQL(t, f, `UPDATE api_token SET scopes=$2::jsonb WHERE token_hash=$1`, crypto.HashToken(token.Secret), scopes)
		after = m9MediaRequestState(t, f)
		m10CookieConfigStatus(t, m9MediaRequestTokenHTTP(t, f, http.MethodGet, m10CookieConfigPath, token.Secret, nil), http.StatusForbidden)
		m9MediaRequestUnchanged(t, f, after)
	}
	m5InteractiveSQL(t, f, `UPDATE app_user SET role='member' WHERE username='creation-admin'`)
	after = m9MediaRequestState(t, f)
	call(http.MethodGet, m10CookieConfigPath, "", nil, admin, http.StatusForbidden)
	call(http.MethodGet, m10CookieConfigPath+"/commands/cloud-config-original", "", nil, admin, http.StatusForbidden)
	call(http.MethodPut, m10CookieConfigPath, "cloud-config-original", body, admin, http.StatusForbidden)
	m9MediaRequestUnchanged(t, f, after)
	if calls.Load() != 0 || metadata.count() != metadataBefore || f.calls.Load() != nativeBefore {
		t.Fatal("configuration or read contacted an upstream service")
	}
	t.Log("M10_COOKIECLOUD_CONFIG_COOKIE_PASS Cookie=REAL App=REAL PG=18.6 original=BYTE_IDENTICAL offline_disable=PRESERVE_CIPHERTEXT scope=OWNER permission=LIVE_DB token=DENIED no_store=ALL protected=FULL_NON_AUTH cloud_metadata_source_calls=0 synchronization_jobs=0")
}

func TestM10CookieCloudConfigStorageFailureHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	admin := m5CreationLogin(t, f, "creation-admin")
	body := m10CookieConfigInput("https://cloud.example")
	m9WordHTTPStable(t, f)
	saved := f.request(t, http.MethodPut, m10CookieConfigPath, "cloud-original", body, admin)
	m10CookieConfigStatus(t, saved, http.StatusOK)
	m9WordHTTPStable(t, f)
	calls := metadata.count()
	m5InteractiveSQL(t, f, `ALTER TABLE cookiecloud_configuration RENAME TO cookiecloud_configuration_fault`)
	restore := true
	defer func() {
		if restore {
			m5InteractiveSQL(t, f, `ALTER TABLE cookiecloud_configuration_fault RENAME TO cookiecloud_configuration`)
		}
	}()
	before := m9MediaRequestState(t, f)
	m10CookieConfigStatus(t, f.request(t, http.MethodGet, m10CookieConfigPath, "", nil, admin), http.StatusServiceUnavailable)
	m10CookieConfigStatus(t, f.request(t, http.MethodPut, m10CookieConfigPath, "new-during-fault", body, admin), http.StatusServiceUnavailable)
	original := f.request(t, http.MethodGet, m10CookieConfigPath+"/commands/cloud-original", "", nil, admin)
	m10CookieConfigStatus(t, original, http.StatusOK)
	replay := f.request(t, http.MethodPut, m10CookieConfigPath, "cloud-original", body, admin)
	m10CookieConfigStatus(t, replay, http.StatusOK)
	if !bytes.Equal(saved.body, original.body) || !bytes.Equal(saved.body, replay.body) {
		t.Fatal("current storage failure changed original command")
	}
	m9MediaRequestUnchanged(t, f, before)
	m5InteractiveSQL(t, f, `ALTER TABLE cookiecloud_configuration_fault RENAME TO cookiecloud_configuration`)
	restore = false
	m5InteractiveSQL(t, f, `CREATE FUNCTION cloud_config_public_fail() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='cookiecloud.configuration' THEN RAISE EXCEPTION 'owned-cloud-store-fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER cloud_config_public_fail BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION cloud_config_public_fail()`)
	before = m9MediaRequestState(t, f)
	update := map[string]any{"expected_version": 1, "enabled": false, "interval_seconds": 1200}
	m10CookieConfigStatus(t, f.request(t, http.MethodPut, m10CookieConfigPath, "cloud-tail", update, admin), http.StatusServiceUnavailable)
	m9MediaRequestUnchanged(t, f, before)
	m5InteractiveSQL(t, f, `DROP TRIGGER cloud_config_public_fail ON audit_log; DROP FUNCTION cloud_config_public_fail()`)
	m10CookieConfigStatus(t, f.request(t, http.MethodPut, m10CookieConfigPath, "cloud-tail", update, admin), http.StatusOK)
	if metadata.count() != calls {
		t.Fatal("storage failure or receipt contacted metadata")
	}
	t.Log("M10_COOKIECLOUD_CONFIG_STORAGE_COOKIE_PASS current_read=503 new_save=ROLLBACK original_GET_and_PUT=BYTE_IDENTICAL tail=FULL_ROLLBACK same_key=RECOVERED protected=FULL_NON_AUTH")
}
