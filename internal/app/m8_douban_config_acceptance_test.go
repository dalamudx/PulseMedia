package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const m8ConfigPath = "/api/v1/sync/douban/config"

func m8ConfigFixture(t *testing.T) (*m7ScrapeFixture, uuid.UUID) {
	t.Helper()
	f := m7NewScrapeFixture(t)
	h := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetDoubanSyncConfigService(watchlist.NewConfigService(postgres.NewDoubanSyncConfigStore(f.pool, f.app.IDGen())))
	f.router = httpapi.NewRouter(f.app, h, f.auth, nil)
	root := uuid.New()
	f.exec(t, `INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,$2,'movie','M8 configuration root',true)`, root, t.TempDir())
	return f, root
}
func m8ConfigInput(root uuid.UUID, version int64, enabled bool) string {
	return fmt.Sprintf(`{"douban_uid":"exherb","enabled":%t,"movie_root_id":%q,"expected_version":%d}`, enabled, root.String(), version)
}
func m8ConfigCode(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("configuration HTTP status=%d want=%d", w.Code, want)
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("configuration cache policy=%q", w.Header().Get("Cache-Control"))
	}
	if want >= 400 && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatal("failure isn't safe problem JSON")
	}
}
func m8ConfigReceipt(t *testing.T, w *httptest.ResponseRecorder) ports.DoubanSyncConfigReceipt {
	t.Helper()
	m8ConfigCode(t, w, http.StatusOK)
	var out ports.DoubanSyncConfigReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Config.ID == uuid.Nil || out.Config.OwnerID == uuid.Nil || out.Config.Version < 1 || out.CommandKey == "" {
		t.Fatal("invalid durable config receipt")
	}
	return out
}
func m8ConfigSnapshot(t *testing.T, f *m7ScrapeFixture, tables ...string) string {
	t.Helper()
	var out strings.Builder
	for _, table := range tables {
		var value string
		query := `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM ` + pgx.Identifier{table}.Sanitize() + ` t) s`
		if err := f.pool.QueryRow(t.Context(), query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		out.WriteString(table)
		out.WriteString(value)
	}
	return out.String()
}
func m8ConfigProtected(t *testing.T, f *m7ScrapeFixture) string {
	t.Helper()
	return m8ConfigSnapshot(t, f, "media", "media_identity", "media_unit", "subscription", "wanted_item", "job", "library_file", "library_file_unit", "unit_fact")
}
func TestM8DoubanConfigPublicCookiePG(t *testing.T) {
	f, root := m8ConfigFixture(t)
	ctx := t.Context()
	protected := m8ConfigProtected(t, f)
	m8ConfigCode(t, f.request(ctx, http.MethodGet, m8ConfigPath, "", "", nil, ""), http.StatusUnauthorized)
	w := f.request(ctx, http.MethodGet, m8ConfigPath, "", "", f.cookie, "")
	m8ConfigCode(t, w, http.StatusOK)
	var absent struct {
		Config *ports.DoubanSyncConfig `json:"config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &absent); err != nil || absent.Config != nil {
		t.Fatal("never-configured state isn't explicit")
	}
	before := m8ConfigSnapshot(t, f, "audit_log", "outbox_event", "idempotency_record", "douban_sync_config", "douban_sync_config_receipt")
	for _, body := range []string{`{}`, `null`, `[]`, `{"douban_uid":"exherb","enabled":true,"expected_version":0,"movie_root_id":null}`, `{"douban_uid":"exherb","enabled":true,"expected_version":0,"movie_root_id":"` + root.String() + `","unknown":1}`, `{"douban_uid":"exherb","douban_uid":"other","enabled":false,"expected_version":0}`} {
		m8ConfigCode(t, f.request(ctx, http.MethodPut, m8ConfigPath, body, "invalid", f.cookie, ""), http.StatusBadRequest)
	}
	if before != m8ConfigSnapshot(t, f, "audit_log", "outbox_event", "idempotency_record", "douban_sync_config", "douban_sync_config_receipt") {
		t.Fatal("invalid admission changed command records")
	}
	body := m8ConfigInput(root, 0, true)
	first := m8ConfigReceipt(t, f.request(ctx, http.MethodPut, m8ConfigPath, body, "original-key", f.cookie, ""))
	if first.Config.OwnerID != f.admin || first.Config.Version != 1 || !first.Config.Enabled || first.Config.MovieRootID == nil || *first.Config.MovieRootID != root {
		t.Fatal("wrong configuration owner/root/version")
	}
	after := m8ConfigSnapshot(t, f, "audit_log", "outbox_event", "idempotency_record", "douban_sync_config", "douban_sync_config_receipt")
	replay := m8ConfigReceipt(t, f.request(ctx, http.MethodPut, m8ConfigPath, body, "original-key", f.cookie, ""))
	if a, b := mustM8ConfigJSON(t, first), mustM8ConfigJSON(t, replay); a != b {
		t.Fatal("same key changed historical result")
	}
	m8ConfigCode(t, f.request(ctx, http.MethodPut, m8ConfigPath, body, "different-key", f.cookie, ""), http.StatusConflict)
	m8ConfigCode(t, f.request(ctx, http.MethodPut, m8ConfigPath, m8ConfigInput(root, 0, false), "original-key", f.cookie, ""), http.StatusConflict)
	if after != m8ConfigSnapshot(t, f, "audit_log", "outbox_event", "idempotency_record", "douban_sync_config", "douban_sync_config_receipt") {
		t.Fatal("replay/conflict changed command records")
	}
	second := m8ConfigReceipt(t, f.request(ctx, http.MethodPut, m8ConfigPath, m8ConfigInput(root, 1, false), "disable-key", f.cookie, ""))
	if second.Config.Version != 2 || second.Config.Enabled || second.Config.ID != first.Config.ID {
		t.Fatal("configuration disable/version failed")
	}
	after = m8ConfigSnapshot(t, f, "audit_log", "outbox_event", "idempotency_record", "douban_sync_config", "douban_sync_config_receipt")
	recovered := m8ConfigReceipt(t, f.request(ctx, http.MethodGet, m8ConfigPath+"/commands/original-key", "", "", f.cookie, ""))
	if mustM8ConfigJSON(t, recovered) != mustM8ConfigJSON(t, first) {
		t.Fatal("read recovery replaced original receipt with current config")
	}
	m8ConfigCode(t, f.request(ctx, http.MethodGet, m8ConfigPath+"/commands/missing-key", "", "", f.cookie, ""), http.StatusNotFound)
	if after != m8ConfigSnapshot(t, f, "audit_log", "outbox_event", "idempotency_record", "douban_sync_config", "douban_sync_config_receipt") {
		t.Fatal("GET created command or event")
	}
	if protected != m8ConfigProtected(t, f) {
		t.Fatal("configuration changed subscription/job/file graph")
	}
	t.Log("M8_DOUBAN_CONFIG_COOKIE_PG_PASS real_cookie=true real_app=true config_only=true subscriptions_created=0")
}
func mustM8ConfigJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestM8DoubanConfigAccountScopePG(t *testing.T) {
	f, root := m8ConfigFixture(t)
	original := m8ConfigReceipt(t, f.request(t.Context(), http.MethodPut, m8ConfigPath, m8ConfigInput(root, 0, true), "original-key", f.cookie, ""))
	actor, err := f.auth.AuthenticateToken(t.Context(), f.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.pool, f.app.IDGen()), crypto.ReadTokenIssuer{}, f.app.Clock())
	_, bearer, err := tokens.Create(t.Context(), actor, ports.TokenCommand{Name: "M8 config read token"}, "m8-config-token")
	if err != nil || bearer == "" {
		t.Fatalf("issue real token: %v", err)
	}
	before := m8ConfigSnapshot(t, f, "douban_sync_config", "douban_sync_config_receipt", "audit_log", "outbox_event")
	for _, route := range []struct{ method, path, body, key string }{
		{http.MethodGet, m8ConfigPath, "", ""},
		{http.MethodGet, m8ConfigPath + "/commands/original-key", "", ""},
		{http.MethodPut, m8ConfigPath, m8ConfigInput(root, 1, false), "token-save"},
	} {
		m8ConfigCode(t, f.request(t.Context(), route.method, route.path, route.body, route.key, nil, bearer), http.StatusForbidden)
	}
	if before != m8ConfigSnapshot(t, f, "douban_sync_config", "douban_sync_config_receipt", "audit_log", "outbox_event") {
		t.Fatal("Token requests changed configuration or history")
	}
	// Clone a fixture account without naming or reading any credential column.
	other := uuid.New()
	f.exec(t, `INSERT INTO app_user SELECT (jsonb_populate_record(NULL::app_user,to_jsonb(u)||jsonb_build_object('id',$1::text,'username','m8-config-other','role','member'))).* FROM app_user u WHERE id=$2`, other, f.admin)
	cookie := f.login(t, "m8-config-other")
	m8ConfigCode(t, f.request(t.Context(), http.MethodGet, m8ConfigPath+"/commands/original-key", "", "", cookie, ""), http.StatusNotFound)
	member := m8ConfigReceipt(t, f.request(t.Context(), http.MethodPut, m8ConfigPath, m8ConfigInput(root, 0, true), "original-key", cookie, ""))
	if member.Config.OwnerID != other || member.Config.ID == original.Config.ID {
		t.Fatal("same command key crossed accounts")
	}
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, other)
	m8ConfigCode(t, f.request(t.Context(), http.MethodGet, m8ConfigPath+"/commands/original-key", "", "", cookie, ""), http.StatusUnauthorized)
	t.Log("M8_DOUBAN_CONFIG_ACCOUNT_SCOPE_PASS member_own_config=true foreign_receipt_hidden=true revoked_cookie=true")
}
func TestM8DoubanConfigFailureAndIsolationPG(t *testing.T) {
	f, root := m8ConfigFixture(t)
	first := m8ConfigReceipt(t, f.request(t.Context(), http.MethodPut, m8ConfigPath, m8ConfigInput(root, 0, true), "original-key", f.cookie, ""))
	before := m8ConfigProtected(t, f)
	f.exec(t, `ALTER TABLE douban_sync_config RENAME TO m8_owned_config_unavailable`)
	m8ConfigCode(t, f.request(t.Context(), http.MethodGet, m8ConfigPath, "", "", f.cookie, ""), http.StatusServiceUnavailable)
	f.exec(t, `ALTER TABLE m8_owned_config_unavailable RENAME TO douban_sync_config`)
	f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=$1 WHERE singleton`, uuid.New())
	m8ConfigCode(t, f.request(t.Context(), http.MethodPut, m8ConfigPath, m8ConfigInput(root, 1, false), "isolated-key", f.cookie, ""), http.StatusServiceUnavailable)
	if before != m8ConfigProtected(t, f) {
		t.Fatal("failure/isolation changed protected graph")
	}
	var version int64
	if err := f.pool.QueryRow(t.Context(), `SELECT version FROM douban_sync_config WHERE id=$1`, first.Config.ID).Scan(&version); err != nil || version != 1 {
		t.Fatalf("isolated configuration changed version=%d error=%v", version, err)
	}
	t.Log("M8_DOUBAN_CONFIG_FAILURE_PG_PASS actual_503=true isolation_blocks_save=true protected_graph_unchanged=true")
}
