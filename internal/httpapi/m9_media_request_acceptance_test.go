package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	authusecase "github.com/dalamudx/PulseMedia/internal/usecase/auth"
	searchusecase "github.com/dalamudx/PulseMedia/internal/usecase/search"
	subscriptionusecase "github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

const m9MediaRequestsPath = "/api/v1/media-requests"

// These tests inherit the opt-in PG18.6 and owned temporary-directory gates.
// All outbound protocols serve synthetic private bytes; no external integration
// or real torrent transfer is claimed. HTTP always reaches the production App.
func m9MediaRequestStatus(t *testing.T, r m5IndexerHTTPResult, status int) {
	t.Helper()
	m5IndexerStatus(t, r, status)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("every media-request response must be private/no-store")
	}
	for _, private := range []string{"m9_owned_private_store_fault", "SQLSTATE", "pgx", "password_hash", "ciphertext"} {
		if bytes.Contains(r.body, []byte(private)) {
			t.Fatal("media-request response exposed private store details")
		}
	}
}

func m9MediaRequestState(t *testing.T, f *m5IndexerAcceptance) map[string][32]byte {
	t.Helper()
	state := m9WordHTTPState(t, f)
	// Token last-use is authentication state, like app_user/app_session.
	delete(state, "api_token")
	return state
}

// Preserve real HTTP/auth/repository behavior while separating GET attribution
// from independently scheduled updates to Wanted after successful ingestion.
func m9MediaRequestStartReader(t *testing.T, f *m5IndexerAcceptance, stop func()) func() {
	t.Helper()
	stop()
	a, err := f.newRuntime()
	if err != nil {
		t.Fatal("read-attribution App construction", err)
	}
	pool := a.DBPool()
	authentication := authusecase.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	handler := new(httpapi.ServerHandler)
	handler.SetMediaRequestService(subscriptionusecase.NewMediaRequestService(postgres.NewMediaRequestStore(pool, a.IDGen(), a.Clock()), func(fence ports.MediaRequestApprovalFence) ports.SubscriptionCreator {
		return subscriptionusecase.NewSubscriptionCreationService(postgres.NewMediaRequestApprovalCreationStore(pool, a.IDGen(), fence), nil, nil, nil)
	}))
	server := httptest.NewServer(httpapi.NewRouter(a, handler, authentication, nil))
	originalBase := f.base
	f.app, f.base = a, server.URL
	var once sync.Once
	closeReader := func() {
		once.Do(func() {
			server.Close()
			if err := a.Close(); err != nil {
				t.Error("close owned read-attribution App", err)
			}
			if f.base == server.URL {
				f.base = originalBase
			}
		})
	}
	t.Cleanup(closeReader)
	return closeReader
}
func m9MediaRequestUnchanged(t *testing.T, f *m5IndexerAcceptance, before map[string][32]byte) {
	t.Helper()
	after := m9MediaRequestState(t, f)
	if !reflect.DeepEqual(before, after) {
		changed := make([]string, 0)
		for table, hash := range before {
			if current, ok := after[table]; !ok || current != hash {
				changed = append(changed, table)
			}
		}
		for table := range after {
			if _, ok := before[table]; !ok {
				changed = append(changed, table)
			}
		}
		t.Fatalf("request read/rejected command changed complete non-authentication business rows: tables=%v", changed)
	}
}

func m9MediaRequestSubmit(t *testing.T, f *m5IndexerAcceptance, session, key string, intent any) (m5IndexerHTTPResult, httpapi.MediaRequestDetails) {
	t.Helper()
	r := f.request(t, http.MethodPost, m9MediaRequestsPath, key, intent, session)
	m9MediaRequestStatus(t, r, http.StatusCreated)
	var receipt httpapi.MediaRequestCommandReceipt
	if err := json.Unmarshal(r.body, &receipt); err != nil || receipt.CommandKey != key || receipt.Request.Id == uuid.Nil || receipt.Request.State != "pending" || receipt.Request.Version != 1 || receipt.Request.SubscriptionId != nil {
		t.Fatal("submission did not return an independent pending-v1 receipt")
	}
	return r, receipt.Request
}

func m9MediaRequestBindings(h *m5AutomaticTVAcceptance) map[string]any {
	return map[string]any{
		"expected_version":  1,
		"root_id":           h.body["root_id"],
		"quality_policy_id": h.body["quality_policy_id"],
		"indexer_ids":       h.body["indexer_ids"],
		"downloader_id":     h.body["downloader_id"],
	}
}

func m9MediaRequestRecover(t *testing.T, f *m5IndexerAcceptance, session, path string, original m5IndexerHTTPResult) {
	t.Helper()
	r := f.request(t, http.MethodGet, path, "", nil, session)
	m9MediaRequestStatus(t, r, http.StatusOK)
	if !bytes.Equal(r.body, original.body) {
		t.Fatal("GET did not recover byte-identical original public POST receipt")
	}
}

func TestM9MediaRequestCookieHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	admin := m5CreationLogin(t, f, "creation-admin")
	memberID, otherID := uuid.New(), uuid.New()
	for _, u := range []struct {
		id   uuid.UUID
		name string
	}{{memberID, "request-member"}, {otherID, "request-other"}} {
		m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,$2,password_hash,'member',true FROM app_user WHERE username='creation-admin'`, u.id, u.name)
	}
	member := m5CreationLogin(t, f, "request-member")
	other := m5CreationLogin(t, f, "request-other")
	m9WordHTTPStable(t, f)
	beforeSubmit := m9MediaRequestState(t, f)
	calls := metadata.count()
	movie := map[string]any{"media_type": "movie", "tmdb_id": 550, "seasons": []int{}, "include_specials": false}
	tv := map[string]any{"media_type": "tv", "tmdb_id": 1399, "seasons": []int{1}, "include_specials": false}
	first, row := m9MediaRequestSubmit(t, f, member, "request-movie", movie)
	tvFirst, tvRow := m9MediaRequestSubmit(t, f, member, "request-tv", tv)
	if row.OwnerId != memberID || tvRow.OwnerId != memberID || !reflect.DeepEqual(tvRow.Intent.Seasons, []int{1}) || tvRow.Intent.TmdbId != 1399 {
		t.Fatal("pending request lost authenticated owner or explicit TV intent")
	}
	// Submit may write only its request and command/audit/outbox evidence.
	afterSubmit := m9MediaRequestState(t, f)
	for table, digest := range beforeSubmit {
		switch table {
		case "media_request", "idempotency_record", "audit_log", "outbox_event":
			continue
		}
		if afterSubmit[table] != digest {
			t.Fatalf("pending submit changed protected table %s", table)
		}
	}
	for _, table := range []string{"subscription", "wanted_item", "download_task", "unit_fact"} {
		if m5InteractiveInt(t, f, "SELECT count(*) FROM "+table) != 0 {
			t.Fatalf("pending submit authorized %s", table)
		}
	}
	if metadata.count() != calls {
		t.Fatal("pending submit contacted metadata")
	}
	m9WordHTTPStable(t, f)
	base := m9MediaRequestState(t, f)
	path := m9MediaRequestsPath + "/" + row.Id.String()
	for _, session := range []string{member, admin} {
		r := f.request(t, http.MethodGet, path, "", nil, session)
		m9MediaRequestStatus(t, r, 200)
		var current httpapi.MediaRequestDetails
		if json.Unmarshal(r.body, &current) != nil || !reflect.DeepEqual(current, row) {
			t.Fatal("current pending request changed its public facts")
		}
		r = f.request(t, http.MethodGet, m9MediaRequestsPath+"?limit=25", "", nil, session)
		m9MediaRequestStatus(t, r, 200)
		var list httpapi.ListMediaRequests200JSONResponse
		if json.Unmarshal(r.body, &list) != nil || len(list.Items) != 2 {
			t.Fatal("public list lost pending requests")
		}
	}
	m9MediaRequestRecover(t, f, member, m9MediaRequestsPath+"/commands/request-movie", first)
	m9MediaRequestRecover(t, f, member, m9MediaRequestsPath+"/commands/request-tv", tvFirst)
	for _, p := range []string{path, m9MediaRequestsPath + "?before_id=" + row.Id.String(), path + "/approval-commands/request-approval"} {
		m9MediaRequestStatus(t, f.request(t, "GET", p, "", nil, other), 403)
	}
	// Command keys are namespaced by the current actor; another account has no such command.
	t.Run("foreign-command-is-outside-current-account", func(t *testing.T) {
		m9MediaRequestStatus(t, f.request(t, "GET", m9MediaRequestsPath+"/commands/request-movie", "", nil, other), http.StatusNotFound)
	})
	closeBody := map[string]any{"expected_version": 1, "action": "withdraw", "reason": "Changed plans"}
	m9MediaRequestStatus(t, f.request(t, "POST", path+"/close", "other-withdraw", closeBody, other), 403)
	m9MediaRequestStatus(t, f.request(t, "POST", path+"/approve", "member-approve", `{}`, member), 403)
	m9MediaRequestStatus(t, f.request(t, "POST", path+"/close", "member-reject", map[string]any{"expected_version": 1, "action": "reject", "reason": "No authority"}, member), 403)
	for _, p := range []string{m9MediaRequestsPath, path, m9MediaRequestsPath + "/commands/request-movie", path + "/approval-commands/request-approval"} {
		m9MediaRequestStatus(t, f.request(t, "GET", p, "", nil, ""), 401)
	}
	m9MediaRequestStatus(t, f.request(t, "POST", m9MediaRequestsPath, "anonymous", movie, ""), 401)
	m9MediaRequestUnchanged(t, f, base)

	invalid := []string{
		`null`, `{}`, `{"media_type":"movie","tmdb_id":550,"seasons":[]}`,
		`{"media_type":"movie","tmdb_id":550,"seasons":null,"include_specials":false}`,
		`{"media_type":"movie","tmdb_id":550,"tmdb_id":551,"seasons":[],"include_specials":false}`,
		`{"media_type":"movie","tmdb_id":550,"\u0074mdb_id":551,"seasons":[],"include_specials":false}`,
		`{"Media_Type":"movie","tmdb_id":550,"seasons":[],"include_specials":false}`,
		`{"media_type":"movie","tmdb_id":550,"seasons":[],"include_specials":false,"owner_id":"` + otherID.String() + `"}`,
		`{"media_type":"tv","tmdb_id":1399,"seasons":[{"number":1,"number":2}],"include_specials":false}`,
		`{"media_type":"movie","tmdb_id":550,"seasons":[],"include_specials":false,"nested":{"x":1,"x":2}}`,
		`{"media_type":"movie","tmdb_id":550,"seasons":[],"include_specials":false,"deep":` + strings.Repeat(`[`, 34) + `0` + strings.Repeat(`]`, 34) + `}`,
	}
	for i, body := range invalid {
		m9MediaRequestStatus(t, f.request(t, "POST", m9MediaRequestsPath, fmt.Sprintf("invalid-request-%d", i), body, member), 400)
	}
	for _, p := range []string{m9MediaRequestsPath + "?limit=", m9MediaRequestsPath + "?limit=01", m9MediaRequestsPath + "?limit=1&limit=2", m9MediaRequestsPath + "?owner_id=" + memberID.String(), m9MediaRequestsPath + "?before_id=bad", m9MediaRequestsPath + "/not-a-uuid", path + "?limit=1", m9MediaRequestsPath + "/commands/bad%20key"} {
		m9MediaRequestStatus(t, f.request(t, "GET", p, "", nil, member), 400)
	}
	for _, key := range []string{"", "bad key", strings.Repeat("a", 81)} {
		m9MediaRequestStatus(t, f.request(t, "POST", m9MediaRequestsPath, key, movie, member), 400)
	}
	m9MediaRequestStatus(t, f.request(t, "POST", m9MediaRequestsPath, "request-movie", tv, member), 409)
	m9MediaRequestUnchanged(t, f, base)
	if metadata.count() != calls {
		t.Fatal("reads or denied input contacted metadata")
	}

	// A genuine publicly issued token remains forbidden even with empty scopes.
	issued := f.request(t, "POST", "/api/v1/auth/tokens", "request-cookie-token", map[string]string{"name": "request-cookie-only"}, member)
	m5IndexerStatus(t, issued, 200)
	var token struct {
		Secret string `json:"secret"`
	}
	if json.Unmarshal(issued.body, &token) != nil || token.Secret == "" {
		t.Fatal("public token issuance failed")
	}
	for _, scopes := range []string{`["read"]`, `[]`, `["read","admin","system","*"]`} {
		m5InteractiveSQL(t, f, `UPDATE api_token SET scopes=$2::jsonb WHERE token_hash=$1`, crypto.HashToken(token.Secret), scopes)
		base = m9MediaRequestState(t, f)
		for _, p := range []string{m9MediaRequestsPath, path, m9MediaRequestsPath + "/commands/request-movie", path + "/approval-commands/request-approval"} {
			m9MediaRequestStatus(t, m9MediaRequestTokenHTTP(t, f, "GET", p, token.Secret, nil), 403)
		}
		m9MediaRequestStatus(t, m9MediaRequestTokenHTTP(t, f, "POST", m9MediaRequestsPath, token.Secret, movie), 403)
		m9MediaRequestStatus(t, m9MediaRequestTokenHTTP(t, f, "POST", path+"/approve", token.Secret, map[string]any{}), 403)
		m9MediaRequestStatus(t, m9MediaRequestTokenHTTP(t, f, "POST", path+"/close", token.Secret, closeBody), 403)
		m9MediaRequestUnchanged(t, f, base)
	}
	closed := f.request(t, "POST", path+"/close", "request-withdraw", closeBody, member)
	m9MediaRequestStatus(t, closed, 200)
	var terminal httpapi.MediaRequestCommandReceipt
	if json.Unmarshal(closed.body, &terminal) != nil || terminal.Request.State != "withdrawn" || terminal.Request.Version != 2 {
		t.Fatal("owner withdrawal was not terminal v2")
	}
	tvPath := m9MediaRequestsPath + "/" + tvRow.Id.String()
	rejected := f.request(t, "POST", tvPath+"/close", "request-reject", map[string]any{"expected_version": 1, "action": "reject", "reason": "Administrator decision"}, admin)
	m9MediaRequestStatus(t, rejected, 200)
	if json.Unmarshal(rejected.body, &terminal) != nil || terminal.Request.State != "rejected" || terminal.Request.Version != 2 {
		t.Fatal("admin rejection was not terminal v2")
	}
	m9WordHTTPStable(t, f)
	base = m9MediaRequestState(t, f)
	m9MediaRequestRecover(t, f, member, m9MediaRequestsPath+"/commands/request-movie", first)
	m9MediaRequestRecover(t, f, member, m9MediaRequestsPath+"/commands/request-tv", tvFirst)
	m9MediaRequestRecover(t, f, member, m9MediaRequestsPath+"/commands/request-withdraw", closed)
	m9MediaRequestStatus(t, f.request(t, "POST", path+"/close", "cannot-reopen", closeBody, member), 409)
	m9MediaRequestUnchanged(t, f, base)
	if metadata.count() != calls {
		t.Fatal("closing or recovering a request queried metadata")
	}

	// Request introduction must preserve existing direct member creation rights.
	direct := f.request(t, "POST", m5CreationPath, "request-direct-member-still-allowed", map[string]any{"tmdb_id": 550}, member)
	m5IndexerStatus(t, direct, 201)
	if m5InteractiveInt(t, f, `SELECT count(*) FROM subscription WHERE id=$1 AND user_id=$2`, m5CreationID(t, direct, "id"), memberID) != 1 {
		t.Fatal("existing direct member creation rights changed")
	}
}

func m9MediaRequestTokenHTTP(t *testing.T, f *m5IndexerAcceptance, method, path, token string, body any) m5IndexerHTTPResult {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, f.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("token request construction failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.base)
	req.Header.Set("Idempotency-Key", "request-token-denied")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 20 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("token HTTP failed")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal("token response read failed")
	}
	return m5IndexerHTTPResult{resp.StatusCode, raw, resp.Header}
}

func TestM9MediaRequestAutomaticTVNativeHTTPPG(t *testing.T) {
	m9MediaRequestNativeFlow(t, false)
}

func m9MediaRequestNativeFlow(t *testing.T, advanced bool) {
	h := newM5AutomaticTVAcceptance(t, false, m9AdvancedFamilyRouting(advanced))
	f := h.f
	admin := m5CreationLogin(t, f, "creation-admin")
	if advanced {
		m9AdvancedFamilyConfigure(t, h, admin)
	}
	m9WordHTTPStable(t, f)
	calls := h.metadata.count()
	// The native creation fixture has scope=all and no explicit season filter.
	intent := map[string]any{"media_type": h.body["media_type"], "tmdb_id": h.body["tmdb_id"], "seasons": []int{}, "include_specials": false}
	pending, row := m9MediaRequestSubmit(t, f, f.session, "request-native-tv-submit", intent)
	if row.OwnerId != h.member || h.metadata.count() != calls || h.adds.Load() != 0 || h.searches.Load() != 0 || h.count(t, `SELECT count(*) FROM subscription`) != 0 || h.count(t, `SELECT count(*) FROM wanted_item`) != 0 || h.count(t, `SELECT count(*) FROM download_task`) != 0 || h.count(t, `SELECT count(*) FROM unit_fact`) != 0 {
		t.Fatal("pending request leaked execution or provider I/O")
	}
	path := m9MediaRequestsPath + "/" + row.Id.String()
	bindings := m9MediaRequestBindings(h)
	m9WordHTTPStable(t, f)
	base := m9MediaRequestState(t, f)
	for _, field := range []string{"owner_id", "user_id", "tmdb_id", "seasons", "scope_type"} {
		body := m9MediaRequestBindings(h)
		body[field] = "client-cannot-set-authority"
		m9MediaRequestStatus(t, f.request(t, "POST", path+"/approve", "approval-injected-"+field, body, admin), 400)
	}
	m9MediaRequestStatus(t, f.request(t, "POST", path+"/approve", "approval-member-denied", bindings, f.session), 403)
	m9MediaRequestUnchanged(t, f, base)
	approved := f.request(t, "POST", path+"/approve", "request-native-tv-approve", bindings, admin)
	m9MediaRequestStatus(t, approved, 201)
	var result httpapi.MediaRequestApprovalResult
	if json.Unmarshal(approved.body, &result) != nil || result.RequestId != row.Id || result.Subscription.Id == uuid.Nil || result.Subscription.MediaId == uuid.Nil || result.Subscription.UserId != h.member {
		t.Fatal("approval did not return member-owned subscription")
	}
	h.subscription, h.media = result.Subscription.Id, result.Subscription.MediaId
	units := m5CreationUnits(t, f, h.media, []string{"S01E01", "S01E03", "S01E07", "S02E09"})
	h.unit = units["S01E03"]
	if h.count(t, `SELECT count(*) FROM subscription WHERE id=$1 AND user_id=$2 AND root_id=$3 AND downloader_id=$4 AND quality_policy_id='m5-automatic-tv'`, h.subscription, h.member, h.root, h.client) != 1 || h.count(t, `SELECT count(*) FROM subscription`) != 1 {
		t.Fatal("approval lost explicit bindings or duplicated subscription")
	}
	if h.count(t, `SELECT count(*) FROM wanted_item WHERE subscription_id=$1`, h.subscription) != 4 || h.count(t, `SELECT count(*) FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id WHERE w.subscription_id=$1 AND u.unit_key NOT IN ('S01E01','S01E03','S01E07','S02E09')`, h.subscription) != 0 || h.count(t, `SELECT count(*) FROM media_unit WHERE media_id=$1 AND unit_key='S01E02'`, h.media) != 0 {
		t.Fatal("approval did not preserve the saved sparse season range")
	}
	if result.Subscription.WantedItems == nil || len(*result.Subscription.WantedItems) != 4 {
		t.Fatal("approval response lost original Wanted snapshot")
	}
	for query, want := range map[string]int{
		`SELECT count(*) FROM media_request WHERE id=$1 AND state='approved' AND version=2 AND subscription_id IS NOT NULL`:                                    1,
		`SELECT count(*) FROM media_request WHERE owner_id=(SELECT owner_id FROM media_request WHERE id=$1) AND state='pending'`:                               0,
		`SELECT count(*) FROM audit_log WHERE action='media_request.approved' AND target_id=$1::text`:                                                          1,
		`SELECT count(*) FROM outbox_event WHERE event_type='media_request.approved' AND aggregate_id=$1::text`:                                                1,
		`SELECT count(*) FROM idempotency_record WHERE idempotency_key='subscription.creation:media.request.approve:'||$1::text||':request-native-tv-approve'`: 1,
	} {
		if h.count(t, query, row.Id) != want {
			t.Fatal("approval transaction evidence mismatch", query)
		}
	}
	if h.count(t, `SELECT count(*) FROM audit_log WHERE action='subscription.creation'`) != 1 || h.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='subscription.created'`) != 1 {
		t.Fatal("approval omitted subscription audit/event")
	}
	changed := m9MediaRequestBindings(h)
	changed["quality_policy_id"] = "default"
	m9MediaRequestStatus(t, f.request(t, "POST", path+"/approve", "request-native-tv-approve", changed, admin), 409)
	m9MediaRequestStatus(t, f.request(t, "POST", path+"/approve", "new-key-cannot-reopen", bindings, admin), 409)
	// Only the existing scheduler consumes committed Wanted and authorizes Add.
	op := h.awaitAdd(t)
	h.complete.Store(true)
	h.unblockAdd()
	h.awaitImported(t, op, 100*time.Second)
	if advanced {
		m9AdvancedFamilyImported(t, h, op, admin)
	}
	m9MediaRequestAssertNativeImport(t, h, op)
	m5AutomaticTVWait(t, 30*time.Second, "request approval ingestion sidecar settled", func(ctx context.Context) (bool, error) {
		var total, active int
		err := f.app.DBPool().QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE state IN ('queued','running')) FROM job WHERE kind='media_sidecar'`).Scan(&total, &active)
		return total == 1 && active == 0, err
	})
	m9WordHTTPStable(t, f)
	approvalPath := path + "/approval-commands/request-native-tv-approve"
	stopReader := m9MediaRequestStartReader(t, f, f.stopRuntime)
	checkReads := func() {
		t.Helper()
		base := m9MediaRequestState(t, f)
		metadata, searches, fetches, adds := h.metadata.count(), h.searches.Load(), h.fetches.Load(), h.adds.Load()
		m9MediaRequestRecover(t, f, admin, approvalPath, approved)
		m9MediaRequestRecover(t, f, f.session, m9MediaRequestsPath+"/commands/request-native-tv-submit", pending)
		current := f.request(t, "GET", path, "", nil, f.session)
		m9MediaRequestStatus(t, current, 200)
		var r httpapi.MediaRequestDetails
		if json.Unmarshal(current.body, &r) != nil || r.State != "approved" || r.Version != 2 || r.SubscriptionId == nil || *r.SubscriptionId != h.subscription {
			t.Fatal("current request did not retain approved-v2 state")
		}
		m9MediaRequestUnchanged(t, f, base)
		if h.metadata.count() != metadata || h.searches.Load() != searches || h.fetches.Load() != fetches || h.adds.Load() != adds {
			t.Fatal("GET performed metadata/source/download I/O")
		}
	}
	checkReads()
	// Make live metadata/config unavailable only after the sidecar is finished.
	m5InteractiveSQL(t, f, `DELETE FROM setting WHERE key='tmdb.api_key'`)
	m5InteractiveSQL(t, f, `UPDATE indexer SET health='offline' WHERE id=$1`, h.indexer)
	m5InteractiveSQL(t, f, `UPDATE library_root SET health='offline' WHERE id=$1`, h.root)
	m5InteractiveSQL(t, f, `UPDATE policy SET current_revision=999999 WHERE id='m5-automatic-tv'`)
	m5InteractiveSQL(t, f, `UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '1 day'`)
	m9WordHTTPStable(t, f)
	checkReads()
	identity := m8AutomaticTVWatchIdentity(t, h, op)
	stopReader()
	a, err := f.newRuntime()
	if err != nil {
		t.Fatal("replacement production App construction failed")
	}
	f.app = a
	restartContext, cancelRestart := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { defer close(done); done <- a.Run(restartContext) }()
	var stopOnce sync.Once
	stopRestart := func() {
		stopOnce.Do(func() {
			cancelRestart()
			select {
			case e := <-done:
				if e != nil {
					t.Error("join restarted production App", e)
				}
			case <-time.After(50 * time.Second):
				t.Error("restarted App shutdown exceeded its join budget")
			}
			if e := a.Close(); e != nil {
				t.Error("close restarted production App", e)
			}
		})
	}
	t.Cleanup(stopRestart)
	client := &http.Client{Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	m5AutomaticTVWait(t, 15*time.Second, "media-request production App restart", func(ctx context.Context) (bool, error) {
		select {
		case <-done:
			return false, errors.New("replacement App exited")
		default:
		}
		req, e := http.NewRequestWithContext(ctx, "GET", f.base+"/api/v1/auth/status", nil)
		if e != nil {
			return false, e
		}
		r, e := client.Do(req)
		if e != nil {
			return false, nil
		}
		defer func() { _ = r.Body.Close() }()
		return r.StatusCode == 200, nil
	})
	m9WordHTTPStable(t, f)
	_ = m9MediaRequestStartReader(t, f, stopRestart)
	checkReads()
	if !bytes.Equal(identity, m8AutomaticTVWatchIdentity(t, h, op)) || h.adds.Load() != 1 {
		t.Fatal("restart replaced authorization/task or repeated physical Add")
	}
	m9MediaRequestAssertNativeImport(t, h, op)
}

// Keep the native fixture's business assertions without replaying its old
// subscription-creation command, which this request approval never issued.
func m9MediaRequestAssertNativeImport(t *testing.T, h *m5AutomaticTVAcceptance, op uuid.UUID) {
	t.Helper()
	if h.adds.Load() != 1 || h.searches.Load() != 1 || h.fetches.Load() != 1 || h.fileReads.Load() < 1 || h.infoReads.Load() < 3 {
		t.Fatal("native protocol cardinality changed")
	}
	var target string
	if err := h.f.app.DBPool().QueryRow(t.Context(), `SELECT f.rel_path FROM library_file f JOIN library_file_unit m ON m.file_id=f.id WHERE m.unit_id=$1 AND f.root_id=$2 AND f.retired_at IS NULL`, h.unit, h.root).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(target) || strings.HasPrefix(target, "..") || !strings.Contains(target, "Season 01") || !strings.Contains(target, "S01E03") {
		t.Fatal("invalid canonical episode path")
	}
	m5AutomaticTVSameFile(t, filepath.Join(h.rootPath, m5AutomaticTVRelease, h.names[0]), filepath.Join(h.rootPath, target), h.payloads[0])
	h.assertSource(t)
	for _, query := range []string{
		`SELECT count(*) FROM unit_fact WHERE unit_id=$1 AND root_id=$2 AND stage='imported' AND revoked_at IS NULL`,
		`SELECT count(*) FROM library_file_unit m JOIN library_file f ON f.id=m.file_id WHERE m.unit_id=$1 AND f.root_id=$2`,
		`SELECT count(*) FROM unit_reservation WHERE unit_id=$1 AND root_id=$2 AND state='consumed'`,
		`SELECT count(*) FROM download_requirement WHERE unit_id=$1 AND root_id=$2 AND state='imported'`,
	} {
		if h.count(t, query, h.unit, h.root) != 1 {
			t.Fatal("final publication cardinality", query)
		}
	}
	for query, want := range map[string]int{
		`SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL`:                                                        0,
		`SELECT count(*) FROM transfer_plan WHERE operation_id=$1`:                                                                                          1,
		`SELECT count(*) FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id WHERE p.operation_id=$1 AND s.state='completed'`:                       1,
		`SELECT count(*) FROM download_task WHERE operation_id=$1`:                                                                                          1,
		`SELECT count(*) FROM automatic_tv_execution_authority WHERE operation_id=$1`:                                                                       1,
		`SELECT count(*) FROM automatic_tv_physical_attempt WHERE operation_id=$1`:                                                                          1,
		`SELECT count(*) FROM automatic_tv_observation_result WHERE operation_id=$1 AND state='completed' AND complete AND jsonb_array_length(inventory)=3`: 1,
	} {
		if h.count(t, query, op) != want {
			t.Fatal("native operation cardinality", query)
		}
	}
	if h.count(t, `SELECT count(*) FROM automatic_tv_round WHERE subscription_id=$1`, h.subscription) != 1 || h.count(t, `SELECT count(*) FROM automatic_tv_execution_authority WHERE subscription_id=$1 AND unit_id<>$2`, h.subscription, h.unit) != 0 || h.count(t, `SELECT count(*) FROM unit_fact f JOIN media_unit u ON u.id=f.unit_id WHERE u.media_id=$1 AND u.id<>$2`, h.media, h.unit) != 0 || h.count(t, `SELECT count(*) FROM wanted_item WHERE subscription_id=$1 AND unit_id<>$2 AND is_satisfied`, h.subscription, h.unit) != 0 {
		t.Fatal("unexpected round/authority/fact or future/unknown-date acquisition")
	}
	if h.count(t, `SELECT count(*) FROM unit_fact WHERE unit_id=$1 AND root_id<>$2 AND stage IN ('imported','confirmed')`, h.unit, h.root) != 0 || h.count(t, `SELECT count(*) FROM episode_candidate_review`) != 0 || h.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`) != 1 {
		t.Fatal("import escaped original authority or duplicated event")
	}
	var statement []byte
	if err := h.f.app.DBPool().QueryRow(t.Context(), `SELECT statement FROM automatic_tv_observation_result WHERE operation_id=$1 AND state='completed'`, op).Scan(&statement); err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Before, After []ports.AutomaticTVRemoteTask
		Files         []ports.AutomaticTVRemoteFile
	}
	if json.Unmarshal(statement, &observed) != nil || len(observed.Before) != 1 || len(observed.After) != 1 || len(observed.Files) != len(h.names) {
		t.Fatal("full immutable A/files/B inventory missing")
	}
	for _, task := range append(observed.Before, observed.After...) {
		if task.InfoHash != h.hash || task.SavePath != h.rootPath || task.TotalBytes != h.total || task.VerifiedBytes != h.total || !task.Complete {
			t.Fatal("native integer totals or task identity changed")
		}
	}
	for i, file := range observed.Files {
		if file.Index != i || file.Path != m5AutomaticTVRelease+"/"+h.names[i] || file.Size != int64(len(h.payloads[i])) || file.CompletedBytes != file.Size || file.Wanted != (i < len(h.names)-1) {
			t.Fatal("inventory lost unwanted/zero-byte entry or changed original bytes")
		}
	}
	if h.total <= int64(len(h.payloads[0])) {
		t.Fatal("inventory did not distinguish video from total bytes")
	}
	_, canonical, digest, err := searchusecase.CanonicalTVEpisodeQuery(ports.TVEpisodeQuery{SeriesID: 550, Keyword: m5AutomaticTVTitle + " 2020", Season: 1, Episode: 3, Categories: []int32{5000}, Offset: 0, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if h.count(t, `SELECT count(*) FROM automatic_tv_query q JOIN indexer_query i ON i.id=q.query_id WHERE i.query_type='tv_episode' AND i.state='succeeded' AND q.parameters=$1::jsonb AND q.parameters_hash=$2 AND q.page=0`, string(canonical), digest) != 1 {
		t.Fatal("exact native episode query proof missing")
	}
}

func TestM9MediaRequestStoreFailureHTTPPG(t *testing.T) {
	h := newM5AutomaticTVAcceptance(t, false)
	f := h.f
	admin := m5CreationLogin(t, f, "creation-admin")
	m9WordHTTPStable(t, f)
	intent := map[string]any{"media_type": "tv", "tmdb_id": 550, "seasons": []int{1}, "include_specials": false}
	pending, row := m9MediaRequestSubmit(t, f, f.session, "request-failure-submit", intent)
	path := m9MediaRequestsPath + "/" + row.Id.String()
	m9WordHTTPStable(t, f)
	base := m9MediaRequestState(t, f)
	// A late approval audit failure occurs after subscription/Wanted/command
	// publication in the same real transaction. The entire graph must roll back.
	clear := m9MediaRequestFault(t, f, "NEW.action='media_request.approved'", "audit_log")
	failed := f.request(t, "POST", path+"/approve", "request-failure-approval", m9MediaRequestBindings(h), admin)
	m9MediaRequestStatus(t, failed, 503)
	if h.count(t, `SELECT count(*) FROM m9_owned_media_request_fault_seen WHERE is_called`) != 1 {
		t.Fatal("approval did not reach the late transaction fault")
	}
	clear()
	m9MediaRequestUnchanged(t, f, base)
	if h.adds.Load() != 0 || h.searches.Load() != 0 {
		t.Fatal("rolled-back approval became visible to scheduler")
	}
	calls := h.metadata.count()
	m9MediaRequestRecover(t, f, f.session, m9MediaRequestsPath+"/commands/request-failure-submit", pending)
	m9MediaRequestStatus(t, f.request(t, "GET", path+"/approval-commands/request-failure-approval", "", nil, admin), 404)
	if h.metadata.count() != calls {
		t.Fatal("failed approval recovery performed metadata I/O")
	}
	m9MediaRequestUnchanged(t, f, base)

	clear = m9MediaRequestFault(t, f, "NEW.media_type='movie'", "media_request")
	failed = f.request(t, "POST", m9MediaRequestsPath, "request-store-new-failure", map[string]any{"media_type": "movie", "tmdb_id": 550, "seasons": []int{}, "include_specials": false}, f.session)
	m9MediaRequestStatus(t, failed, 503)
	if h.count(t, `SELECT count(*) FROM m9_owned_media_request_fault_seen WHERE is_called`) != 1 {
		t.Fatal("new request did not reach the real store fault")
	}
	clear()
	m9MediaRequestUnchanged(t, f, base)
	if h.metadata.count() != calls || h.adds.Load() != 0 {
		t.Fatal("failed new request performed external I/O")
	}

	// Remove only this test database's table name, then reset pooled prepared
	// statements. This is a real read/storage outage, not an injected service.
	m5InteractiveSQL(t, f, `ALTER TABLE media_request RENAME TO m9_owned_media_request_unavailable`)
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer cancel()
			if _, err := f.app.DBPool().Exec(ctx, `ALTER TABLE m9_owned_media_request_unavailable RENAME TO media_request`); err != nil {
				t.Error("restore owned media-request table", err)
				return
			}
			f.app.DBPool().Reset()
		})
	}
	t.Cleanup(restore)
	f.app.DBPool().Reset()
	for _, p := range []string{m9MediaRequestsPath, path, m9MediaRequestsPath + "/commands/request-failure-submit"} {
		m9MediaRequestStatus(t, f.request(t, "GET", p, "", nil, f.session), 503)
	}
	m9MediaRequestStatus(t, f.request(t, "POST", m9MediaRequestsPath, "request-table-unavailable", intent, f.session), 503)
	restore()
	m9MediaRequestUnchanged(t, f, base)
	m9MediaRequestRecover(t, f, f.session, m9MediaRequestsPath+"/commands/request-failure-submit", pending)
	m9MediaRequestUnchanged(t, f, base)
	if h.metadata.count() != calls || h.searches.Load() != 0 || h.fetches.Load() != 0 || h.adds.Load() != 0 {
		t.Fatal("storage failure/recovery performed provider or download I/O")
	}

	// The failed submit key was never committed. Retry it with real movie
	// bindings and prove approval uses trusted metadata and the requesting owner.
	movieIntent := map[string]any{"media_type": "movie", "tmdb_id": 550, "seasons": []int{}, "include_specials": false}
	moviePending, movieRow := m9MediaRequestSubmit(t, f, f.session, "request-store-new-failure", movieIntent)
	var movieRoot uuid.UUID
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type='movie' AND health='online' AND baseline_completed`).Scan(&movieRoot); err != nil {
		t.Fatal(err)
	}
	movieBindings := m9MediaRequestBindings(h)
	movieBindings["root_id"] = movieRoot
	moviePath := m9MediaRequestsPath + "/" + movieRow.Id.String()
	movieApproved := f.request(t, "POST", moviePath+"/approve", "request-real-movie-approve", movieBindings, admin)
	m9MediaRequestStatus(t, movieApproved, 201)
	var movieResult httpapi.MediaRequestApprovalResult
	if json.Unmarshal(movieApproved.body, &movieResult) != nil || movieResult.RequestId != movieRow.Id || movieResult.Subscription.UserId != h.member || movieResult.Subscription.RootId != movieRoot || movieResult.Subscription.WantedItems == nil || len(*movieResult.Subscription.WantedItems) != 1 {
		t.Fatal("movie approval lost owner/root/Wanted")
	}
	m5CreationUnits(t, f, movieResult.Subscription.MediaId, []string{"movie"})
	if h.count(t, `SELECT count(*) FROM media WHERE id=$1 AND title='Native trusted movie' AND year=1999 AND media_type='movie'`, movieResult.Subscription.MediaId) != 1 || h.count(t, `SELECT count(*) FROM subscription`) != 1 || h.count(t, `SELECT count(*) FROM media_request WHERE id=$1 AND state='approved' AND version=2 AND subscription_id=$2`, movieRow.Id, movieResult.Subscription.Id) != 1 {
		t.Fatal("movie approval did not atomically publish trusted subscription and terminal request")
	}
	m9WordHTTPStable(t, f)
	base = m9MediaRequestState(t, f)
	calls = h.metadata.count()
	m9MediaRequestRecover(t, f, admin, moviePath+"/approval-commands/request-real-movie-approve", movieApproved)
	m9MediaRequestRecover(t, f, f.session, m9MediaRequestsPath+"/commands/request-store-new-failure", moviePending)
	m9MediaRequestUnchanged(t, f, base)
	if h.metadata.count() != calls || h.adds.Load() != 0 {
		t.Fatal("movie receipt recovery contacted provider or downloader")
	}
}

func m9MediaRequestFault(t *testing.T, f *m5IndexerAcceptance, condition, table string) func() {
	t.Helper()
	// Both identifiers/conditions are owned constants, never user inputs.
	m5InteractiveSQL(t, f, `CREATE SEQUENCE m9_owned_media_request_fault_seen; CREATE FUNCTION m9_owned_media_request_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN PERFORM nextval('m9_owned_media_request_fault_seen'); RAISE EXCEPTION 'm9_owned_private_store_fault' USING ERRCODE='23514'; END IF; RETURN NEW; END $$`)
	var once sync.Once
	clear := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer cancel()
			if _, err := f.app.DBPool().Exec(ctx, `DROP TRIGGER IF EXISTS m9_owned_media_request_fault ON `+table+`; DROP FUNCTION IF EXISTS m9_owned_media_request_fault(); DROP SEQUENCE IF EXISTS m9_owned_media_request_fault_seen`); err != nil {
				t.Error("owned media-request fault cleanup", err)
			}
		})
	}
	t.Cleanup(clear)
	m5InteractiveSQL(t, f, `CREATE TRIGGER m9_owned_media_request_fault BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION m9_owned_media_request_fault()`)
	return clear
}
