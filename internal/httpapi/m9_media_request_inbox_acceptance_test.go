package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/google/uuid"
)

func TestM9MediaRequestInboxCookieHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	admin := m5CreationLogin(t, f, "creation-admin")
	for _, name := range []string{"inbox-member", "inbox-other"} {
		m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,$2,password_hash,'member',true FROM app_user WHERE username='creation-admin'`, uuid.New(), name)
	}
	member := m5CreationLogin(t, f, "inbox-member")
	other := m5CreationLogin(t, f, "inbox-other")
	m9WordHTTPStable(t, f)
	summaryPath := m9MediaRequestsPath + "/summary"
	readCount := func(want string) {
		t.Helper()
		result := f.request(t, http.MethodGet, summaryPath, "", nil, admin)
		m9MediaRequestStatus(t, result, 200)
		var value httpapi.GetMediaRequestSummary200JSONResponse
		if err := json.Unmarshal(result.body, &value); err != nil || value.PendingCount != want || value.ObservedAt.IsZero() {
			t.Fatal("summary count/time", value, err)
		}
	}
	before := m9MediaRequestState(t, f)
	calls := metadata.count()
	readCount("0")
	m9MediaRequestUnchanged(t, f, before)
	movie := map[string]any{"media_type": "movie", "tmdb_id": 550, "seasons": []int{}, "include_specials": false}
	for i := range 33 {
		session := member
		if i >= 30 {
			session = other
		}
		m9MediaRequestSubmit(t, f, session, fmt.Sprintf("inbox-public-%d", i), movie)
	}
	m9WordHTTPStable(t, f)
	before = m9MediaRequestState(t, f)
	readCount("33")
	readPage := func(path string, want int) []httpapi.MediaRequestDetails {
		t.Helper()
		result := f.request(t, http.MethodGet, path, "", nil, admin)
		m9MediaRequestStatus(t, result, 200)
		var page struct {
			Items []httpapi.MediaRequestDetails `json:"items"`
		}
		if err := json.Unmarshal(result.body, &page); err != nil || len(page.Items) != want {
			t.Fatal("pending page", len(page.Items), err)
		}
		for _, row := range page.Items {
			if row.State != "pending" {
				t.Fatal("terminal request included")
			}
		}
		return page.Items
	}
	first := readPage(m9MediaRequestsPath+"?state=pending&limit=25", 25)
	cursor := first[24].Id
	second := readPage(m9MediaRequestsPath+"?state=pending&limit=25&before_id="+cursor.String(), 8)
	seen := map[uuid.UUID]bool{}
	for _, rows := range [][]httpapi.MediaRequestDetails{first, second} {
		for _, row := range rows {
			if seen[row.Id] {
				t.Fatal("duplicate pending row")
			}
			seen[row.Id] = true
		}
	}
	for _, tc := range []struct {
		session, path string
		code          int
	}{{"", summaryPath, 401}, {member, summaryPath, 403}, {member, m9MediaRequestsPath + "?state=pending", 403}, {admin, m9MediaRequestsPath + "?state=approved", 400}, {admin, m9MediaRequestsPath + "?state=pending&state=pending", 400}, {admin, summaryPath + "?limit=25", 400}} {
		m9MediaRequestStatus(t, f.request(t, http.MethodGet, tc.path, "", nil, tc.session), tc.code)
	}
	m9MediaRequestUnchanged(t, f, before)
	close := f.request(t, http.MethodPost, m9MediaRequestsPath+"/"+cursor.String()+"/close", "inbox-public-close", map[string]any{"expected_version": 1, "action": "reject", "reason": "explicit moderation"}, admin)
	m9MediaRequestStatus(t, close, 200)
	before = m9MediaRequestState(t, f)
	readCount("32")
	readPage(m9MediaRequestsPath+"?state=pending&limit=100", 32)
	readPage(m9MediaRequestsPath+"?state=pending&before_id="+cursor.String(), 8)
	m9MediaRequestUnchanged(t, f, before)
	m5InteractiveSQL(t, f, `UPDATE app_user SET role='member' WHERE username='creation-admin'`)
	before = m9MediaRequestState(t, f)
	m9MediaRequestStatus(t, f.request(t, http.MethodGet, summaryPath, "", nil, admin), 403)
	m9MediaRequestStatus(t, f.request(t, http.MethodGet, m9MediaRequestsPath+"?state=pending", "", nil, admin), 403)
	m9MediaRequestUnchanged(t, f, before)
	if metadata.count() != calls {
		t.Fatal("inbox GET or pending request contacted metadata")
	}
	t.Log("M9_INBOX_COOKIE_HTTP_PASS count=GLOBAL_33_32_0 pending=25_PLUS_8 terminal_cursor=true no_store=ALL access=LIVE_DB readonly=FULL_NON_AUTH metadata=0")
}

func TestM9MediaRequestInboxStoreFailureHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	admin := m5CreationLogin(t, f, "creation-admin")
	m9WordHTTPStable(t, f)
	m5InteractiveSQL(t, f, `ALTER TABLE media_request RENAME TO media_request_inbox_fault`)
	defer m5InteractiveSQL(t, f, `ALTER TABLE media_request_inbox_fault RENAME TO media_request`)
	before := m9MediaRequestState(t, f)
	calls := metadata.count()
	for _, path := range []string{m9MediaRequestsPath + "/summary", m9MediaRequestsPath + "?state=pending"} {
		m9MediaRequestStatus(t, f.request(t, http.MethodGet, path, "", nil, admin), 503)
	}
	m9MediaRequestUnchanged(t, f, before)
	if metadata.count() != calls {
		t.Fatal("failed inbox GET contacted metadata")
	}
}
