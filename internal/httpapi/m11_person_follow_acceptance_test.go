package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func personFollowAcceptanceInput(t *testing.T, f *m5IndexerAcceptance) ports.PersonFollowInput {
	t.Helper()
	var root uuid.UUID
	var revision int64
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type='movie' AND health='online' AND baseline_completed ORDER BY id LIMIT 1`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT current_revision FROM policy WHERE id='default'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return ports.PersonFollowInput{Settings: domain.Settings{PersonID: 525, Role: "director", Enabled: true, MinReleaseDate: "2026-10-01", IntervalMinutes: 1440, MovieRootID: &root, QualityPolicyID: "default", QualityPolicyRevision: revision}}
}
func personFollowAcceptanceStatus(t *testing.T, r m5IndexerHTTPResult, status int) {
	t.Helper()
	m5IndexerStatus(t, r, status)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("follow response may be cached")
	}
	for _, unsafe := range []string{"SQLSTATE", "owned_person_tail_private", "api_key", "password", "ciphertext"} {
		if bytes.Contains(r.body, []byte(unsafe)) {
			t.Fatal("follow response exposed internal data")
		}
	}
}
func personFollowAcceptanceReceipt(t *testing.T, r m5IndexerHTTPResult) httpapi.PersonFollowReceipt {
	t.Helper()
	personFollowAcceptanceStatus(t, r, http.StatusOK)
	var receipt httpapi.PersonFollowReceipt
	if json.Unmarshal(r.body, &receipt) != nil || receipt.CommandKey == "" || receipt.Config.Id == uuid.Nil || receipt.Config.OwnerId == uuid.Nil || receipt.Config.Version <= 0 || receipt.Config.CreatedAt.IsZero() {
		t.Fatal("invalid follow receipt")
	}
	return receipt
}
func personFollowAcceptanceOnlyConfigChanged(t *testing.T, f *m5IndexerAcceptance, before map[string][32]byte) {
	t.Helper()
	after := m9MediaRequestState(t, f)
	for _, table := range []string{"person_follow_config", "person_follow_indexer", "person_follow_config_history", "audit_log", "outbox_event", "idempotency_record"} {
		delete(before, table)
		delete(after, table)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("saving follow intent changed protected business tables")
	}
}
func TestM11PersonFollowConfigurationCookiePG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	owner := uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'person-follow-member',password_hash,'member',true FROM app_user WHERE username='creation-admin'`, owner)
	member := m5CreationLogin(t, f, "person-follow-member")
	m9WordHTTPStable(t, f)
	calls := metadata.count()
	before := m9MediaRequestState(t, f)
	input := personFollowAcceptanceInput(t, f)
	id := uuid.New()
	path := "/api/v1/person-follows/" + id.String()
	saved := personFollowAcceptanceReceipt(t, f.request(t, http.MethodPut, path, "follow-create", input, member))
	if saved.Config.Id != id || saved.Config.OwnerId != owner || saved.Config.Version != 1 || saved.Config.Settings.PersonId != 525 || saved.Config.Settings.Role != httpapi.Director {
		t.Fatal("public intent changed identity or account")
	}
	personFollowAcceptanceOnlyConfigChanged(t, f, before)
	baseline := m9MediaRequestState(t, f)
	restored := personFollowAcceptanceReceipt(t, f.request(t, http.MethodPut, path, "follow-create", input, member))
	if !reflect.DeepEqual(saved, restored) {
		t.Fatal("original save changed")
	}
	historical := personFollowAcceptanceReceipt(t, f.request(t, http.MethodGet, "/api/v1/person-follows/receipts/follow-create", "", nil, member))
	if !reflect.DeepEqual(saved, historical) {
		t.Fatal("original receipt changed")
	}
	personFollowAcceptanceStatus(t, f.request(t, http.MethodGet, path, "", nil, member), http.StatusOK)
	listed := f.request(t, http.MethodGet, "/api/v1/person-follows", "", nil, member)
	personFollowAcceptanceStatus(t, listed, http.StatusOK)
	var rows []httpapi.PersonFollowConfiguration
	if json.Unmarshal(listed.body, &rows) != nil || len(rows) != 1 || rows[0].Id != id || rows[0].OwnerId != owner {
		t.Fatal("member listing lost scope")
	}
	otherList := f.request(t, http.MethodGet, "/api/v1/person-follows", "", nil, f.session)
	personFollowAcceptanceStatus(t, otherList, http.StatusOK)
	if json.Unmarshal(otherList.body, &rows) != nil || len(rows) != 0 {
		t.Fatal("administrator listing included another account's follows")
	}
	personFollowAcceptanceStatus(t, f.request(t, http.MethodGet, path, "", nil, f.session), http.StatusNotFound)
	personFollowAcceptanceStatus(t, f.request(t, http.MethodGet, "/api/v1/person-follows/receipts/follow-create", "", nil, f.session), http.StatusNotFound)
	personFollowAcceptanceStatus(t, f.request(t, http.MethodPut, path, "foreign-update", input, f.session), http.StatusNotFound)
	personFollowAcceptanceStatus(t, f.request(t, http.MethodPut, path, "stale-version", input, member), http.StatusConflict)
	m9MediaRequestUnchanged(t, f, baseline)
	input.ExpectedVersion = 1
	input.Settings.Enabled = false
	disabled := personFollowAcceptanceReceipt(t, f.request(t, http.MethodPut, path, "follow-disable", input, member))
	if disabled.Config.Version != 2 || disabled.Config.Settings.Enabled {
		t.Fatal("disable did not save a new version")
	}
	baseline = m9MediaRequestState(t, f)
	original := personFollowAcceptanceReceipt(t, f.request(t, http.MethodGet, "/api/v1/person-follows/receipts/follow-create", "", nil, member))
	if !reflect.DeepEqual(original, saved) {
		t.Fatal("current version overwrote original receipt")
	}
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=false WHERE id=$1`, owner)
	personFollowAcceptanceStatus(t, f.request(t, http.MethodGet, path, "", nil, member), http.StatusUnauthorized)
	personFollowAcceptanceStatus(t, f.request(t, http.MethodGet, "/api/v1/person-follows/receipts/follow-create", "", nil, member), http.StatusUnauthorized)
	m9MediaRequestUnchanged(t, f, baseline)
	if metadata.count() != calls {
		t.Fatal("configuration access requested external metadata")
	}
	t.Log("M11_PERSON_CONFIG_COOKIE_PASS App=PRODUCTION Cookie=REAL actor=MEMBER ownership=EXACT saving=ZERO_TASKS original_key=READ_ONLY versions=FROZEN revocation=IMMEDIATE protected=UNCHANGED external=ZERO")
}
func TestM11PersonFollowConfigurationRollbackCookiePG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	m9WordHTTPStable(t, f)
	calls := metadata.count()
	input := personFollowAcceptanceInput(t, f)
	id := uuid.New()
	path := "/api/v1/person-follows/" + id.String()
	m5InteractiveSQL(t, f, `CREATE FUNCTION person_follow_cookie_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned_person_tail_private'; END $$; CREATE TRIGGER person_follow_cookie_fault BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION person_follow_cookie_fault()`)
	baseline := m9MediaRequestState(t, f)
	personFollowAcceptanceStatus(t, f.request(t, http.MethodPut, path, "follow-tail", input, f.session), http.StatusServiceUnavailable)
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, `DROP TRIGGER person_follow_cookie_fault ON idempotency_record; DROP FUNCTION person_follow_cookie_fault()`)
	saved := personFollowAcceptanceReceipt(t, f.request(t, http.MethodPut, path, "follow-tail", input, f.session))
	m5InteractiveSQL(t, f, `ALTER TABLE person_follow_config RENAME TO person_follow_config_owned_unavailable`)
	baseline = m9MediaRequestState(t, f)
	personFollowAcceptanceStatus(t, f.request(t, http.MethodGet, "/api/v1/person-follows", "", nil, f.session), http.StatusServiceUnavailable)
	personFollowAcceptanceStatus(t, f.request(t, http.MethodGet, path, "", nil, f.session), http.StatusServiceUnavailable)
	old := personFollowAcceptanceReceipt(t, f.request(t, http.MethodGet, "/api/v1/person-follows/receipts/follow-tail", "", nil, f.session))
	if !reflect.DeepEqual(old, saved) {
		t.Fatal("receipt depended on live configuration table")
	}
	replay := personFollowAcceptanceReceipt(t, f.request(t, http.MethodPut, path, "follow-tail", input, f.session))
	if !reflect.DeepEqual(replay, saved) {
		t.Fatal("same-key recovery read the live configuration")
	}
	m9MediaRequestUnchanged(t, f, baseline)
	m5InteractiveSQL(t, f, `ALTER TABLE person_follow_config_owned_unavailable RENAME TO person_follow_config`)
	if metadata.count() != calls {
		t.Fatal("failure recovery requested external metadata")
	}
	t.Log("M11_PERSON_CONFIG_ROLLBACK_COOKIE_PASS App=PRODUCTION Cookie=REAL tail=FULL_ROLLBACK original_key=REUSABLE storage=503 old_receipt=OFFLINE_READ_ONLY protected=UNCHANGED external=ZERO")
}
