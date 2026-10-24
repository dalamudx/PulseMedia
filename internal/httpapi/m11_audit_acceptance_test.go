package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/google/uuid"
)

const m11AuditPath = "/api/v1/system/audit"

func m11AuditStatus(t *testing.T, r m5IndexerHTTPResult, status int) {
	t.Helper()
	m5IndexerStatus(t, r, status)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("audit response may be cached")
	}
	for _, secret := range []string{"owned-private-audit", "owned-audit-new-api-key", "owned-audit-next-api-key", "owned_audit_tail_private", "SQLSTATE", "password_hash", "ciphertext", `"payload"`} {
		if bytes.Contains(r.body, []byte(secret)) {
			t.Fatal("private audit payload or storage data exposed")
		}
	}
}
func m11AuditPage(t *testing.T, r m5IndexerHTTPResult) httpapi.AuditTimelinePage {
	t.Helper()
	m11AuditStatus(t, r, http.StatusOK)
	var page httpapi.AuditTimelinePage
	if err := json.Unmarshal(r.body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Items == nil {
		t.Fatal("audit items must be an array")
	}
	return page
}

func TestM11AuditCookiePaginationAndPermissionPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	memberID, otherID := uuid.New(), uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'audit-member',password_hash,'member',true FROM app_user WHERE username='creation-admin'`, memberID)
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'audit-other',password_hash,'admin',true FROM app_user WHERE username='creation-admin'`, otherID)
	member, other := m5CreationLogin(t, f, "audit-member"), m5CreationLogin(t, f, "audit-other")
	issued := f.request(t, http.MethodPost, "/api/v1/auth/tokens", "audit-read-token", map[string]string{"name": "audit-read"}, f.session)
	m5IndexerStatus(t, issued, http.StatusOK)
	var token struct {
		Secret string `json:"secret"`
	}
	if json.Unmarshal(issued.body, &token) != nil || token.Secret == "" {
		t.Fatal("public read token missing")
	}
	at := time.Date(2030, 1, 2, 3, 4, 5, 123456000, time.UTC)
	expected := make(map[uuid.UUID]bool, 53)
	for range 53 {
		id := uuid.New()
		expected[id] = true
		m5InteractiveSQL(t, f, `INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload,created_at) VALUES($1,'user',$2,'owned.audit','media','exact_target','{"cookie":"owned-private-audit","download_url":"owned-private-audit"}',$3)`, id, memberID.String(), at)
	}
	m9WordHTTPStable(t, f)
	baseline := m9MediaRequestState(t, f)
	calls := metadata.count()
	query := url.Values{"action": {"owned.audit"}, "actor_type": {"user"}, "actor_id": {memberID.String()}, "target_type": {"media"}, "target_id": {"exact_target"}, "from": {at.Format(time.RFC3339Nano)}, "until": {at.Add(time.Second).Format(time.RFC3339Nano)}}
	page := m11AuditPage(t, f.request(t, http.MethodGet, m11AuditPath+"?"+query.Encode(), "", nil, f.session))
	if len(page.Items) != 50 || page.NextCursor == nil {
		t.Fatal("default page boundary", len(page.Items))
	}
	seen := map[uuid.UUID]bool{}
	for _, row := range page.Items {
		if !expected[row.Id] || seen[row.Id] || row.ActorId != memberID.String() || row.Action != "owned.audit" || !row.CreatedAt.Equal(at) {
			t.Fatal("incorrect summary")
		}
		seen[row.Id] = true
	}
	m9MediaRequestUnchanged(t, f, baseline)
	cursor := *page.NextCursor
	query.Set("cursor", cursor)
	// Retention may delete an already returned row; a later insertion must not enter this continuation.
	m5InteractiveSQL(t, f, `DELETE FROM audit_log WHERE id=$1`, page.Items[len(page.Items)-1].Id)
	m5InteractiveSQL(t, f, `INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload,created_at) VALUES($1,'user',$2,'owned.audit','media','exact_target','{}',$3)`, uuid.New(), memberID.String(), at.Add(time.Microsecond))
	baseline = m9MediaRequestState(t, f)
	page = m11AuditPage(t, m9MediaRequestTokenHTTP(t, f, http.MethodGet, m11AuditPath+"?"+query.Encode(), token.Secret, nil))
	if len(page.Items) != 3 || page.NextCursor != nil {
		t.Fatal("read token continuation", len(page.Items))
	}
	for _, row := range page.Items {
		if !expected[row.Id] || seen[row.Id] {
			t.Fatal("duplicate or injected head")
		}
		seen[row.Id] = true
	}
	if len(seen) != 53 {
		t.Fatal("public pagination lost rows")
	}
	for _, tc := range []struct {
		session string
		status  int
	}{{"", http.StatusUnauthorized}, {member, http.StatusForbidden}, {other, http.StatusBadRequest}} {
		m11AuditStatus(t, f.request(t, http.MethodGet, m11AuditPath+"?"+query.Encode(), "", nil, tc.session), tc.status)
	}
	for _, bad := range []string{"?Limit=1", "?limit=1&limit=2", "?limit=201", "?action=", "?from=bad"} {
		m11AuditStatus(t, f.request(t, http.MethodGet, m11AuditPath+bad, "", nil, f.session), http.StatusBadRequest)
	}
	empty := m11AuditPage(t, f.request(t, http.MethodGet, m11AuditPath+"?action=absent.audit", "", nil, f.session))
	if len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatal("empty filter result")
	}
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, `UPDATE app_user SET role='member' WHERE username='creation-admin'`)
	baseline = m9MediaRequestState(t, f)
	m11AuditStatus(t, f.request(t, http.MethodGet, m11AuditPath, "", nil, f.session), http.StatusForbidden)
	m11AuditStatus(t, m9MediaRequestTokenHTTP(t, f, http.MethodGet, m11AuditPath, token.Secret, nil), http.StatusForbidden)
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=false WHERE username='creation-admin'`)
	baseline = m9MediaRequestState(t, f)
	m11AuditStatus(t, f.request(t, http.MethodGet, m11AuditPath, "", nil, f.session), http.StatusUnauthorized)
	m11AuditStatus(t, m9MediaRequestTokenHTTP(t, f, http.MethodGet, m11AuditPath, token.Secret, nil), http.StatusUnauthorized)
	m9MediaRequestUnchanged(t, f, baseline)
	if metadata.count() != calls {
		t.Fatal("audit read called metadata")
	}
	t.Log("M11_AUDIT_COOKIE_PASS App=PRODUCTION Cookie=REAL Token=PUBLIC_READ pagination=50+3 cursor=RETENTION_SAFE permission=LIVE read_only=ALL_NON_AUTH payload=EXCLUDED external=ZERO")
}

func TestM11AuditBusinessAtomicityAndUnavailablePG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	m9WordHTTPStable(t, f)
	calls := metadata.count()
	count := func() int { return m5InteractiveInt(t, f, `SELECT count(*) FROM audit_log`) }
	initial := count()
	save := func(key string, version int, secret string) m5IndexerHTTPResult {
		return f.request(t, http.MethodPut, "/api/v1/settings/tmdb.api_key", key, map[string]any{"expected_version": version, "value": map[string]any{"api_key": secret}}, f.session)
	}
	saved := save("audit-save", 1, "owned-audit-new-api-key")
	m11AuditStatus(t, saved, http.StatusOK)
	m9WordHTTPStable(t, f)
	if count() != initial+1 {
		t.Fatal("business save did not write one audit")
	}
	baseline := m9MediaRequestState(t, f)
	recovered := save("audit-save", 1, "owned-audit-new-api-key")
	m11AuditStatus(t, recovered, http.StatusOK)
	m9MediaRequestUnchanged(t, f, baseline)
	var id uuid.UUID
	var actorType, actorID, action, targetType, targetID string
	var createdAt time.Time
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT id,actor_type,actor_id,action,target_type,target_id,created_at FROM audit_log ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&id, &actorType, &actorID, &action, &targetType, &targetID, &createdAt); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"actor_id": {actorID}, "action": {action}, "target_type": {targetType}, "target_id": {targetID}, "limit": {"1"}}
	page := m11AuditPage(t, f.request(t, http.MethodGet, m11AuditPath+"?"+query.Encode(), "", nil, f.session))
	if len(page.Items) != 1 || page.Items[0].Id != id || page.Items[0].ActorType != actorType || !page.Items[0].CreatedAt.Equal(createdAt) {
		t.Fatal("public summary does not match transactional audit")
	}
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, `CREATE FUNCTION m11_audit_tail_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned_audit_tail_private'; END $$; CREATE TRIGGER m11_audit_tail_fail BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION m11_audit_tail_fail()`)
	baseline = m9MediaRequestState(t, f)
	m11AuditStatus(t, save("audit-tail-fail", 2, "owned-audit-next-api-key"), http.StatusServiceUnavailable)
	m9MediaRequestUnchanged(t, f, baseline)
	if count() != initial+1 {
		t.Fatal("rolled-back business change left success audit")
	}
	m5InteractiveSQL(t, f, `DROP TRIGGER m11_audit_tail_fail ON idempotency_record; DROP FUNCTION m11_audit_tail_fail()`)
	m11AuditStatus(t, save("audit-tail-fail", 2, "owned-audit-next-api-key"), http.StatusOK)
	m9WordHTTPStable(t, f)
	if count() != initial+2 {
		t.Fatal("rollback consumed original key or duplicated audit")
	}
	// Invalidate cached SQL plans as well as fresh reads; failure must not become an empty timeline.
	m5InteractiveSQL(t, f, `ALTER TABLE audit_log RENAME TO audit_log_owned_unavailable`)
	baseline = m9MediaRequestState(t, f)
	m11AuditStatus(t, f.request(t, http.MethodGet, m11AuditPath, "", nil, f.session), http.StatusServiceUnavailable)
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, `ALTER TABLE audit_log_owned_unavailable RENAME TO audit_log`)
	baseline = m9MediaRequestState(t, f)
	page = m11AuditPage(t, f.request(t, http.MethodGet, m11AuditPath, "", nil, f.session))
	if len(page.Items) == 0 {
		t.Fatal("restored audit store returned empty")
	}
	m9MediaRequestUnchanged(t, f, baseline)
	if metadata.count() != calls {
		t.Fatal("settings or audit operations called metadata")
	}
	t.Log("M11_AUDIT_ATOMIC_PASS App=PRODUCTION Cookie=REAL audit=BUSINESS_TRANSACTION original_key=ONE_RECORD tail=FULL_ROLLBACK unavailable=503 recovery=READ_ONLY external=ZERO")
}
