package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/google/uuid"
)

func m10SeedConfigStatus(t *testing.T, r m5IndexerHTTPResult, status int) {
	t.Helper()
	m5IndexerStatus(t, r, status)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("seeding configuration response was cacheable", r.header.Get("Cache-Control"))
	}
	for _, private := range []string{"owned-seeding-credential", "private-seeding-prefix", "owned-seeding-store-fault", "SQLSTATE", "pgx"} {
		if bytes.Contains(r.body, []byte(private)) {
			t.Fatal("seeding configuration exposed private data")
		}
	}
}
func m10SeedConfigClient(t *testing.T, f *m5IndexerAcceptance, admin, url string) httpapi.DownloadClientDetails {
	t.Helper()
	r := f.request(t, http.MethodPost, "/api/v1/download-clients", "seed-native-client", map[string]any{"name": "Owned seeding client", "client_type": "qbittorrent", "url": url + "/private-seeding-prefix", "username": "owned-seeding-user", "password": "owned-seeding-credential"}, admin)
	m5IndexerStatus(t, r, http.StatusCreated)
	var client httpapi.DownloadClientDetails
	if json.Unmarshal(r.body, &client) != nil || client.Id == uuid.Nil || client.UpdatedAt.IsZero() {
		t.Fatal("native client creation response missing identity or current timestamp")
	}
	return client
}
func m10SeedConfigInput(client httpapi.DownloadClientDetails) map[string]any {
	return map[string]any{"expected_version": 0, "expected_client_updated_at": client.UpdatedAt, "enabled": true, "interval_seconds": 1200, "rule": map[string]any{"mode": "any", "delete_files": false, "min_share_ratio": 2}}
}
func m10SeedConfigOnlyChanges(t *testing.T, before, after map[string][32]byte) {
	t.Helper()
	allowed := map[string]bool{"seeding_configuration": true, "audit_log": true, "outbox_event": true, "idempotency_record": true}
	for table, hash := range before {
		if !allowed[table] && after[table] != hash {
			t.Fatal("configuration save altered unrelated table", table)
		}
	}
	if len(before) != len(after) {
		t.Fatal("configuration save altered table inventory")
	}
}
func TestM10SeedingConfigCookieHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	admin := m5CreationLogin(t, f, "creation-admin")
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	client := m10SeedConfigClient(t, f, admin, remote.URL)
	path := "/api/v1/download-clients/" + client.Id.String() + "/seeding/config"
	for _, entry := range []struct{ name, role string }{{"seeding-other-admin", "admin"}, {"seeding-member", "member"}} {
		m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,$2,password_hash,$3,true FROM app_user WHERE username='creation-admin'`, uuid.New(), entry.name, entry.role)
	}
	other := m5CreationLogin(t, f, "seeding-other-admin")
	member := m5CreationLogin(t, f, "seeding-member")
	call := func(method, route, key string, body any, session string, status int) m5IndexerHTTPResult {
		t.Helper()
		r := f.request(t, method, route, key, body, session)
		m10SeedConfigStatus(t, r, status)
		return r
	}
	m9WordHTTPStable(t, f)
	before := m9MediaRequestState(t, f)
	metadataBefore, nativeBefore := metadata.count(), f.calls.Load()
	initial := call(http.MethodGet, path, "", nil, admin, http.StatusOK)
	if strings.TrimSpace(string(initial.body)) != `{"config":null}` {
		t.Fatal("missing seeding configuration not explicit null")
	}
	body := m10SeedConfigInput(client)
	saved := call(http.MethodPut, path, "seed-original", body, admin, http.StatusOK)
	after := m9MediaRequestState(t, f)
	m10SeedConfigOnlyChanges(t, before, after)
	var receipt httpapi.SeedingConfigReceipt
	if json.Unmarshal(saved.body, &receipt) != nil || receipt.Config.ClientId != client.Id || receipt.Config.Version != 1 || receipt.Config.Rule.MinShareRatio == nil || *receipt.Config.Rule.MinShareRatio != 2 || bool(receipt.Config.Rule.DeleteFiles) || !receipt.Config.ClientUpdatedAt.Equal(client.UpdatedAt) {
		t.Fatal("saved rule snapshot changed")
	}
	original := call(http.MethodGet, path+"/commands/seed-original", "", nil, admin, http.StatusOK)
	replay := call(http.MethodPut, path, "seed-original", body, admin, http.StatusOK)
	if !bytes.Equal(saved.body, original.body) || !bytes.Equal(saved.body, replay.body) {
		t.Fatal("original bytes changed")
	}
	current := call(http.MethodGet, path, "", nil, other, http.StatusOK)
	var view httpapi.SeedingConfigRead
	if json.Unmarshal(current.body, &view) != nil || view.Config == nil || view.Config.OwnerId != receipt.Config.OwnerId {
		t.Fatal("current admin configuration not shared with recorded owner")
	}
	call(http.MethodGet, path+"/commands/seed-original", "", nil, other, http.StatusNotFound)
	call(http.MethodGet, path+"?unexpected=true", "", nil, admin, http.StatusBadRequest)
	call(http.MethodGet, path, "", nil, "", http.StatusUnauthorized)
	call(http.MethodGet, path, "", nil, member, http.StatusForbidden)
	call(http.MethodPut, path, "member-save", body, member, http.StatusForbidden)
	call(http.MethodPut, path, "stale-rule", body, admin, http.StatusConflict)
	for _, bad := range []string{strings.Replace(mustSeedJSON(t, body), `"enabled":true`, `"enabled":true,"enabled":false`, 1), strings.Replace(mustSeedJSON(t, body), `"delete_files":false`, `"delete_files":true`, 1), strings.Replace(mustSeedJSON(t, body), `"min_share_ratio":2`, `"min_share_ratio":2,"min_share_ratio":3`, 1)} {
		call(http.MethodPut, path, "invalid", bad, admin, http.StatusBadRequest)
	}
	if !reflect.DeepEqual(after, m9MediaRequestState(t, f)) {
		t.Fatal("read/replayed/rejected commands changed complete non-auth state")
	}
	m5InteractiveSQL(t, f, `UPDATE download_client SET url='https://offline.invalid',updated_at=updated_at+interval '1 second' WHERE id=$1`, client.Id)
	before = m9MediaRequestState(t, f)
	body["expected_client_updated_at"] = client.UpdatedAt.In(time.FixedZone("owned", 8*3600))
	original = call(http.MethodPut, path, "seed-original", body, admin, http.StatusOK)
	if !bytes.Equal(original.body, saved.body) {
		t.Fatal("offline timezone-equivalent original changed")
	}
	call(http.MethodPut, path, "stale-client", body, admin, http.StatusConflict)
	if !reflect.DeepEqual(before, m9MediaRequestState(t, f)) {
		t.Fatal("offline recovery or stale configuration wrote")
	}
	var updated time.Time
	if err := f.app.DBPool().QueryRow(t.Context(), "SELECT updated_at FROM download_client WHERE id=$1", client.Id).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	body["expected_version"] = 1
	body["expected_client_updated_at"] = updated
	body["enabled"] = false
	call(http.MethodPut, path, "offline-disable", body, admin, http.StatusOK)
	m9WordHTTPStable(t, f)
	original = call(http.MethodGet, path+"/commands/seed-original", "", nil, admin, http.StatusOK)
	if !bytes.Equal(original.body, saved.body) {
		t.Fatal("deactivation changed original receipt")
	}
	issued := f.request(t, http.MethodPost, "/api/v1/auth/tokens", "seeding-token", map[string]string{"name": "seeding-cookie-only"}, admin)
	m5IndexerStatus(t, issued, http.StatusOK)
	var token struct {
		Secret string `json:"secret"`
	}
	if json.Unmarshal(issued.body, &token) != nil || token.Secret == "" {
		t.Fatal("real token issuance failed")
	}
	for _, scopes := range []string{`["read","write"]`, `[]`, `["read","write","admin","system","*"]`} {
		m5InteractiveSQL(t, f, `UPDATE api_token SET scopes=$2::jsonb WHERE token_hash=$1`, crypto.HashToken(token.Secret), scopes)
		before = m9MediaRequestState(t, f)
		m10SeedConfigStatus(t, m9MediaRequestTokenHTTP(t, f, http.MethodGet, path, token.Secret, nil), http.StatusForbidden)
		m9MediaRequestUnchanged(t, f, before)
	}
	m5InteractiveSQL(t, f, `UPDATE app_user SET role='member' WHERE username='creation-admin'`)
	before = m9MediaRequestState(t, f)
	call(http.MethodGet, path, "", nil, admin, http.StatusForbidden)
	call(http.MethodGet, path+"/commands/seed-original", "", nil, admin, http.StatusForbidden)
	call(http.MethodPut, path, "seed-original", body, admin, http.StatusForbidden)
	m9MediaRequestUnchanged(t, f, before)
	if calls.Load() != 0 || metadata.count() != metadataBefore || f.calls.Load() != nativeBefore {
		t.Fatal("seeding configuration called native downloader or metadata")
	}
	t.Log("M10_SEEDING_CONFIG_COOKIE_PASS Cookie=REAL App=REAL PG=18.6 scope=CLIENT_AND_ACCOUNT original=BYTE_IDENTICAL timezone=INSTANT offline_disable=NO_IO permission=LIVE_DB token=DENIED no_store=ALL protected=FULL_NON_AUTH downloader_calls=0 execution_jobs=UNCHANGED")
}
func mustSeedJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestM10SeedingConfigStorageFailureHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	admin := m5CreationLogin(t, f, "creation-admin")
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	client := m10SeedConfigClient(t, f, admin, remote.URL)
	path := "/api/v1/download-clients/" + client.Id.String() + "/seeding/config"
	body := m10SeedConfigInput(client)
	m9WordHTTPStable(t, f)
	metadataBefore := metadata.count()
	saved := f.request(t, http.MethodPut, path, "seed-original", body, admin)
	m10SeedConfigStatus(t, saved, http.StatusOK)
	m9WordHTTPStable(t, f)
	m5InteractiveSQL(t, f, `ALTER TABLE seeding_configuration RENAME TO seeding_configuration_fault`)
	restore := true
	defer func() {
		if restore {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := f.app.DBPool().Exec(ctx, `ALTER TABLE seeding_configuration_fault RENAME TO seeding_configuration`); err != nil {
				t.Error("configuration fault cleanup failed", err)
			}
		}
	}()
	before := m9MediaRequestState(t, f)
	m10SeedConfigStatus(t, f.request(t, http.MethodGet, path, "", nil, admin), http.StatusServiceUnavailable)
	m10SeedConfigStatus(t, f.request(t, http.MethodPut, path, "new-during-fault", body, admin), http.StatusServiceUnavailable)
	original := f.request(t, http.MethodGet, path+"/commands/seed-original", "", nil, admin)
	m10SeedConfigStatus(t, original, http.StatusOK)
	replay := f.request(t, http.MethodPut, path, "seed-original", body, admin)
	m10SeedConfigStatus(t, replay, http.StatusOK)
	if !bytes.Equal(original.body, saved.body) || !bytes.Equal(replay.body, saved.body) {
		t.Fatal("current store fault changed original")
	}
	m9MediaRequestUnchanged(t, f, before)
	m5InteractiveSQL(t, f, `ALTER TABLE seeding_configuration_fault RENAME TO seeding_configuration`)
	restore = false
	m5InteractiveSQL(t, f, `CREATE FUNCTION seed_config_public_fail() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='seeding.configuration' THEN RAISE EXCEPTION 'owned-seeding-store-fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER seed_config_public_fail BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION seed_config_public_fail()`)
	body["expected_version"] = 1
	body["enabled"] = false
	before = m9MediaRequestState(t, f)
	m10SeedConfigStatus(t, f.request(t, http.MethodPut, path, "seed-tail", body, admin), http.StatusServiceUnavailable)
	m9MediaRequestUnchanged(t, f, before)
	m5InteractiveSQL(t, f, `DROP TRIGGER seed_config_public_fail ON audit_log; DROP FUNCTION seed_config_public_fail()`)
	m10SeedConfigStatus(t, f.request(t, http.MethodPut, path, "seed-tail", body, admin), http.StatusOK)
	if metadata.count() != metadataBefore || calls.Load() != 0 {
		t.Fatal("storage failure or original receipt called upstream")
	}
	t.Log("M10_SEEDING_CONFIG_STORAGE_COOKIE_PASS current_read=503 new_save=ROLLBACK original_GET_and_PUT=BYTE_IDENTICAL tail=FULL_ROLLBACK same_key=RECOVERED protected=FULL_NON_AUTH native_calls=0")
}
