package app

import (
	"bytes"
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
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
)

type m8CheckinConfigFixture struct{ *m8StatsConfigFixture }

func m8NewCheckinConfigFixture(t *testing.T) *m8CheckinConfigFixture {
	t.Helper()
	f := m8NewStatsConfigFixture(t)
	secrets := crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32))
	h := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetSiteCheckinConfigService(site.NewCheckinConfigService(postgres.NewSiteCheckinConfigStore(f.pool, f.app.IDGen(), secrets)))
	f.router = httpapi.NewRouter(f.app, h, f.auth, nil)
	return &m8CheckinConfigFixture{f}
}
func (f *m8CheckinConfigFixture) path() string {
	return "/api/v1/indexers/" + f.source.ID.String() + "/checkin/config"
}
func (f *m8CheckinConfigFixture) body(version, source int64, enabled bool) string {
	return fmt.Sprintf(`{"expected_account_id":"42","profile":"nexusphp_1103_attendance_v1","timezone":"Asia/Shanghai","daily_at":"09:00","enabled":%t,"expected_version":%d,"expected_source_version":%d}`, enabled, version, source)
}
func (f *m8CheckinConfigFixture) records(t *testing.T) string {
	return m8ConfigSnapshot(t, f.m7ScrapeFixture, "site_checkin_config", "site_checkin_config_receipt", "audit_log", "outbox_event", "job")
}
func m8CheckinConfigReceipt(t *testing.T, w *httptest.ResponseRecorder) ports.SiteCheckinConfigReceipt {
	t.Helper()
	m8ConfigCode(t, w, http.StatusOK)
	var r ports.SiteCheckinConfigReceipt
	if e := json.Unmarshal(w.Body.Bytes(), &r); e != nil || r.Config.IndexerID == uuid.Nil || r.Config.Version < 1 || r.CommandKey == "" {
		t.Fatal("invalid daily check-in configuration receipt", e)
	}
	if strings.Contains(w.Body.String(), "owned-account-fixture") || strings.Contains(w.Body.String(), "http://") {
		t.Fatal("configuration exposed source credentials")
	}
	return r
}

func TestM8SiteCheckinConfigCookiePG(t *testing.T) {
	f := m8NewCheckinConfigFixture(t)
	ctx := t.Context()
	protected := m8ConfigProtected(t, f.m7ScrapeFixture)
	m8ConfigCode(t, f.request(ctx, http.MethodGet, f.path(), "", "", nil, ""), http.StatusUnauthorized)
	w := f.request(ctx, http.MethodGet, f.path(), "", "", f.cookie, "")
	m8ConfigCode(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != `{"config":null}` {
		t.Fatal("configuration absence lost")
	}
	before := f.records(t)
	for _, body := range []string{`{}`, `null`, `[]`, `{"expected_account_id":"42","expected_account_id":"99"}`, strings.TrimSuffix(f.body(0, 1, true), "}") + `,"cookie":"injected"}`} {
		m8ConfigCode(t, f.request(ctx, http.MethodPut, f.path(), body, "invalid-key", f.cookie, ""), http.StatusBadRequest)
	}
	if before != f.records(t) {
		t.Fatal("invalid requests changed configuration graph")
	}
	input := f.body(0, f.source.ConfigVersion, true)
	first := m8CheckinConfigReceipt(t, f.request(ctx, http.MethodPut, f.path(), input, "original-key", f.cookie, ""))
	if first.Config.IndexerID != f.source.ID || first.Config.ExpectedAccountID != "42" || !first.Config.Enabled || first.Config.SourceConfigVersion != f.source.ConfigVersion {
		t.Fatal("saved wrong source configuration")
	}
	before = f.records(t)
	replay := m8CheckinConfigReceipt(t, f.request(ctx, http.MethodPut, f.path(), input, "original-key", f.cookie, ""))
	if mustM8ConfigJSON(t, first) != mustM8ConfigJSON(t, replay) {
		t.Fatal("same original key changed result")
	}
	m8ConfigCode(t, f.request(ctx, http.MethodPut, f.path(), input, "different-key", f.cookie, ""), http.StatusConflict)
	if before != f.records(t) {
		t.Fatal("replay or conflict wrote records")
	}
	second := m8CheckinConfigReceipt(t, f.request(ctx, http.MethodPut, f.path(), f.body(1, f.source.ConfigVersion, false), "disable-key", f.cookie, ""))
	if second.Config.Enabled || second.Config.Version != 2 {
		t.Fatal("disable did not advance version")
	}
	actor, e := f.auth.AuthenticateToken(ctx, f.cookie.Value)
	if e != nil {
		t.Fatal(e)
	}
	changed, e := f.manager.Update(ctx, actor, f.source.ID, ports.IndexerManagementInput{Name: "Owned account statistics source", Protocol: ports.IndexerProtocolNexusPHP, Categories: []int{}, ExpectedVersion: f.source.ConfigVersion}, "update-source")
	if e != nil {
		t.Fatal("update original source", e)
	}
	before = f.records(t)
	recovered := m8CheckinConfigReceipt(t, f.request(ctx, http.MethodGet, f.path()+"/commands/original-key", "", "", f.cookie, ""))
	if mustM8ConfigJSON(t, recovered) != mustM8ConfigJSON(t, first) {
		t.Fatal("source edit changed original receipt")
	}
	m8ConfigCode(t, f.request(ctx, http.MethodPut, f.path(), f.body(2, f.source.ConfigVersion, true), "stale-source-key", f.cookie, ""), http.StatusConflict)
	if before != f.records(t) {
		t.Fatal("read recovery or stale-source request wrote records")
	}
	third := m8CheckinConfigReceipt(t, f.request(ctx, http.MethodPut, f.path(), f.body(2, changed.Indexer.ConfigVersion, true), "new-source-key", f.cookie, ""))
	if third.Config.Version != 3 || third.Config.SourceConfigVersion != changed.Indexer.ConfigVersion {
		t.Fatal("source version was not explicitly rebound")
	}
	if protected != m8ConfigProtected(t, f.m7ScrapeFixture) || f.calls.Load() != 0 {
		t.Fatal("saving statistics settings changed business data or called source")
	}
	t.Log("M8_CHECKIN_CONFIG_COOKIE_PG_PASS real_cookie=true explicit_source=true configuration_only=true network_calls=0")
}

func TestM8SiteCheckinConfigPermissionsPG(t *testing.T) {
	f := m8NewCheckinConfigFixture(t)
	ctx := t.Context()
	m8CheckinConfigReceipt(t, f.request(ctx, http.MethodPut, f.path(), f.body(0, f.source.ConfigVersion, true), "original-key", f.cookie, ""))
	actor, e := f.auth.AuthenticateToken(ctx, f.cookie.Value)
	if e != nil {
		t.Fatal(e)
	}
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.pool, f.app.IDGen()), crypto.ReadTokenIssuer{}, f.app.Clock())
	_, bearer, e := tokens.Create(ctx, actor, ports.TokenCommand{Name: "Owned statistics configuration read token"}, "stats-token")
	if e != nil {
		t.Fatal(e)
	}
	other := uuid.New()
	f.exec(t, `INSERT INTO app_user SELECT (jsonb_populate_record(NULL::app_user,to_jsonb(u)||jsonb_build_object('id',$1::text,'username','stats-other','role','member'))).* FROM app_user u WHERE id=$2`, other, f.admin)
	member := f.login(t, "stats-other")
	before := f.records(t)
	for _, identity := range []struct {
		cookie *http.Cookie
		token  string
	}{{nil, bearer}, {member, ""}} {
		m8ConfigCode(t, f.request(ctx, http.MethodGet, f.path(), "", "", identity.cookie, identity.token), http.StatusForbidden)
		m8ConfigCode(t, f.request(ctx, http.MethodGet, f.path()+"/commands/original-key", "", "", identity.cookie, identity.token), http.StatusForbidden)
		m8ConfigCode(t, f.request(ctx, http.MethodPut, f.path(), f.body(1, f.source.ConfigVersion, false), "forbidden-key", identity.cookie, identity.token), http.StatusForbidden)
	}
	f.exec(t, `UPDATE app_user SET role='admin' WHERE id=$1`, other)
	m8ConfigCode(t, f.request(ctx, http.MethodGet, f.path(), "", "", member, ""), http.StatusOK)
	m8ConfigCode(t, f.request(ctx, http.MethodGet, f.path()+"/commands/original-key", "", "", member, ""), http.StatusNotFound)
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, other)
	m8ConfigCode(t, f.request(ctx, http.MethodGet, f.path(), "", "", member, ""), http.StatusUnauthorized)
	if before != f.records(t) || f.calls.Load() != 0 {
		t.Fatal("forbidden or read requests changed configuration or called source")
	}
	t.Log("M8_CHECKIN_CONFIG_PERMISSION_PG_PASS member_token_denied=true global_admin_read=true foreign_receipt_hidden=true revoked_cookie=true")
}

func TestM8SiteCheckinConfigReadFailureAndIsolationPG(t *testing.T) {
	f := m8NewCheckinConfigFixture(t)
	ctx := t.Context()
	m8CheckinConfigReceipt(t, f.request(ctx, http.MethodPut, f.path(), f.body(0, f.source.ConfigVersion, true), "original-key", f.cookie, ""))
	protected := m8ConfigProtected(t, f.m7ScrapeFixture)
	before := f.records(t)
	f.exec(t, `ALTER TABLE site_checkin_config RENAME TO m8_owned_checkin_config_unavailable`)
	m8ConfigCode(t, f.request(ctx, http.MethodGet, f.path(), "", "", f.cookie, ""), http.StatusServiceUnavailable)
	f.exec(t, `ALTER TABLE m8_owned_checkin_config_unavailable RENAME TO site_checkin_config`)
	f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=$1 WHERE singleton`, uuid.New())
	m8ConfigCode(t, f.request(ctx, http.MethodPut, f.path(), f.body(1, f.source.ConfigVersion, false), "isolated-key", f.cookie, ""), http.StatusServiceUnavailable)
	if before != f.records(t) || protected != m8ConfigProtected(t, f.m7ScrapeFixture) || f.calls.Load() != 0 {
		t.Fatal("read failure or isolation published settings or network I/O")
	}
	t.Log("M8_CHECKIN_CONFIG_FAILURE_PG_PASS actual_read_503=true isolation_blocks_save=true protected_graph_unchanged=true network_calls=0")
}
