package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestSeerrApprovedAndAutoApprovedCookieHTTPPG(t *testing.T) {
	// CI's isolated PG partitions must not depend on the historical milestone gate.
	if os.Getenv("PULSE_TEST_DATABASE_URL") == "" && os.Getenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE") != "1" {
		t.Skip("an explicitly owned PG18.6 fixture is required")
	}
	t.Setenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE", "1")
	f, metadata := newM5CreationAcceptance(t)
	const configPath = "/api/v1/settings/seerr"
	const ingressPath = "/api/v1/integrations/seerr"
	const privateEmail = "seerr-inbound-private@example.invalid"
	const privateURL = "https://seerr-private.invalid/request/secret"
	secret := strings.Repeat("owned-seerr-auto-credential-", 2)
	safe := func(t *testing.T, r m5IndexerHTTPResult, want int) {
		t.Helper()
		for _, private := range []string{secret, privateEmail, privateURL, "requestedBy_email", "credential_encrypted"} {
			if bytes.Contains(r.body, []byte(private)) {
				t.Fatal("HTTP response exposed private Seerr input")
			}
		}
		m5IndexerStatus(t, r, want)
		if r.header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("Seerr response must be private/no-store")
		}
	}
	var owner uuid.UUID
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM app_user WHERE username='creation-admin'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	safe(t, f.request(t, http.MethodGet, configPath, "", nil, f.session), http.StatusOK)
	saved := f.request(t, http.MethodPut, configPath, "", map[string]any{
		"enabled": true, "expected_version": 0, "owner_id": owner.String(), "credential": secret,
	}, f.session)
	safe(t, saved, http.StatusOK)
	var cfg ports.SeerrConfiguration
	if err := json.Unmarshal(saved.body, &cfg); err != nil || !cfg.Enabled || cfg.Version != 1 || !cfg.CredentialPresent || cfg.OwnerID == nil || *cfg.OwnerID != owner {
		t.Fatal("real Cookie configuration did not persist")
	}
	var cipher []byte
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT credential_encrypted FROM seerr_configuration`).Scan(&cipher); err != nil || len(cipher) == 0 || bytes.Contains(cipher, []byte(secret)) {
		t.Fatal("dedicated credential was not encrypted", err)
	}
	issued := f.request(t, http.MethodPost, "/api/v1/auth/tokens", "seerr-auto-read-token", map[string]string{"name": "seerr-auto-read"}, f.session)
	m5IndexerStatus(t, issued, http.StatusOK)
	var token struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(issued.body, &token); err != nil || token.Secret == "" {
		t.Fatal("real API Token issuance failed")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	// This sends to App.Run without a Cookie or Origin unless explicitly testing a session.
	send := func(raw, credential, session string) (m5IndexerHTTPResult, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.base+ingressPath, strings.NewReader(raw))
		if err != nil {
			return m5IndexerHTTPResult{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		if credential != "" {
			req.Header.Set("Authorization", credential)
		}
		if session != "" {
			req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		}
		resp, err := client.Do(req)
		if err != nil {
			return m5IndexerHTTPResult{}, err
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Error("close Seerr ingress response", closeErr)
			}
		}()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return m5IndexerHTTPResult{status: resp.StatusCode, body: body, header: resp.Header}, err
	}
	payload := func(event, kind string, id int, seasons bool) string {
		extra := ""
		if seasons {
			extra = `,"extra":[{"name":"Requested Seasons","value":"2, 1"}]`
		}
		return fmt.Sprintf(`{"notification_type":%q,"media":{"media_type":%q,"tmdbId":"550"},"request":{"request_id":"%d","requestedBy_email":%q,"requestedBy_role":"admin"},"subject":%q,"image":%q%s}`, event, kind, id, privateEmail, "Untrusted inbound title", privateURL, extra)
	}
	snapshot := func(t *testing.T) string {
		t.Helper()
		var raw string
		err := f.app.DBPool().QueryRow(t.Context(), `SELECT jsonb_build_object(
   'requests',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM media_request x),
   'receipts',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY external_request_id),'[]') FROM seerr_ingress_receipt x),
   'audit',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM audit_log x WHERE action IN ('seerr.request.received','media.request.submitted')),
   'commands',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM idempotency_record x WHERE idempotency_key LIKE '%seerr:%'),
   'events',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,event_type,payload) ORDER BY id),'[]') FROM outbox_event WHERE event_type LIKE 'media.request.%'),
   'subscriptions',(SELECT count(*) FROM subscription),
   'downloads',(SELECT count(*) FROM download_task))::text`).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	calls := metadata.count()
	receipts := map[uuid.UUID]bool{}
	for i, tc := range []struct{ event, kind string }{
		{"MEDIA_APPROVED", "movie"}, {"MEDIA_AUTO_APPROVED", "movie"},
		{"MEDIA_APPROVED", "tv"}, {"MEDIA_AUTO_APPROVED", "tv"},
	} {
		t.Run(tc.event+"/"+tc.kind, func(t *testing.T) {
			raw := payload(tc.event, tc.kind, 1100+i, tc.kind == "tv")
			var wg sync.WaitGroup
			results := make([]m5IndexerHTTPResult, 2)
			errs := make([]error, 2)
			for n := range 2 {
				wg.Add(1)
				go func(n int) { defer wg.Done(); results[n], errs[n] = send(raw, secret, "") }(n)
			}
			wg.Wait()
			for n := range 2 {
				if errs[n] != nil {
					t.Fatal("dedicated HTTP request failed", errs[n])
				}
				safe(t, results[n], http.StatusAccepted)
			}
			if !bytes.Equal(results[0].body, results[1].body) {
				t.Fatal("concurrent delivery changed receipt")
			}
			var receipt ports.SeerrReceipt
			if err := json.Unmarshal(results[0].body, &receipt); err != nil || receipt.RequestID == uuid.Nil || receipt.State != "pending" || receipt.ConfigVersion != 1 || receipt.ExternalRequestID != fmt.Sprint(1100+i) || receipt.CreatedAt.IsZero() {
				t.Fatal("invalid pending ingress receipt")
			}
			if receipts[receipt.RequestID] {
				t.Fatal("distinct external request reused a local request")
			}
			receipts[receipt.RequestID] = true
			var state, kind string
			var version, tmdb int64
			var actualOwner uuid.UUID
			var seasons []int
			var pendingOnly bool
			err := f.app.DBPool().QueryRow(t.Context(), `SELECT state,version,owner_id,media_type,tmdb_id,seasons,
    subscription_id IS NULL AND decision_actor_id IS NULL AND decided_at IS NULL AND NOT include_specials
    FROM media_request WHERE id=$1`, receipt.RequestID).Scan(&state, &version, &actualOwner, &kind, &tmdb, &seasons, &pendingOnly)
			wantSeasons := []int{}
			if tc.kind == "tv" {
				wantSeasons = []int{1, 2}
			}
			if err != nil || state != "pending" || version != 1 || actualOwner != owner || kind != tc.kind || tmdb != 550 || !reflect.DeepEqual(seasons, wantSeasons) || !pendingOnly {
				t.Fatal("external approval bypassed or changed local pending intent", err)
			}
			before := snapshot(t)
			retry, err := send(raw, secret, "")
			if err != nil {
				t.Fatal(err)
			}
			safe(t, retry, http.StatusAccepted)
			if !bytes.Equal(retry.body, results[0].body) || snapshot(t) != before {
				t.Fatal("retry mutated original receipt or request")
			}
			for _, changed := range []string{
				strings.Replace(raw, `"tmdbId":"550"`, `"tmdbId":"551"`, 1),
				strings.Replace(raw, tc.event, map[string]string{"MEDIA_APPROVED": "MEDIA_AUTO_APPROVED", "MEDIA_AUTO_APPROVED": "MEDIA_APPROVED"}[tc.event], 1),
			} {
				conflict, err := send(changed, secret, "")
				if err != nil {
					t.Fatal(err)
				}
				safe(t, conflict, http.StatusConflict)
				if snapshot(t) != before {
					t.Fatal("conflicting external ID wrote business state")
				}
			}
			safe(t, f.request(t, http.MethodGet, m9MediaRequestsPath+"/"+receipt.RequestID.String(), "", nil, f.session), http.StatusOK)
		})
	}
	if len(receipts) != 4 {
		t.Fatal("not all approval/media combinations completed")
	}
	before := snapshot(t)
	for _, tc := range []struct {
		name, raw, credential, session string
		want                           int
	}{
		{"available", payload("MEDIA_AVAILABLE", "movie", 1200, false), secret, "", 400},
		{"requested", payload("MEDIA_REQUESTED", "movie", 1201, false), secret, "", 400},
		{"auto-tv-no-seasons", payload("MEDIA_AUTO_APPROVED", "tv", 1202, false), secret, "", 400},
		{"session-only", payload("MEDIA_AUTO_APPROVED", "movie", 1203, false), "", f.session, 401},
		{"ordinary-token", payload("MEDIA_AUTO_APPROVED", "movie", 1204, false), "Bearer " + token.Secret, "", 401},
		{"wrong-credential", payload("MEDIA_AUTO_APPROVED", "movie", 1205, false), strings.Repeat("wrong-seerr-credential-", 2), "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := send(tc.raw, tc.credential, tc.session)
			if err != nil {
				t.Fatal(err)
			}
			safe(t, result, tc.want)
			if snapshot(t) != before {
				t.Fatal("rejected ingress wrote business state")
			}
		})
	}
	safe(t, f.request(t, http.MethodGet, configPath, "", nil, f.session), http.StatusOK)
	listed := f.request(t, http.MethodGet, "/api/v1/seerr/receipts", "", nil, f.session)
	safe(t, listed, http.StatusOK)
	var list struct {
		Items []ports.SeerrReceipt `json:"items"`
	}
	if err := json.Unmarshal(listed.body, &list); err != nil || len(list.Items) != 4 {
		t.Fatal("receipt read lost accepted events")
	}
	for _, receipt := range list.Items {
		if !receipts[receipt.RequestID] || receipt.State != "pending" {
			t.Fatal("unexpected receipt projection")
		}
	}
	safe(t, m9MediaRequestTokenHTTP(t, f, http.MethodGet, configPath, token.Secret, nil), http.StatusForbidden)
	safe(t, m9MediaRequestTokenHTTP(t, f, http.MethodGet, "/api/v1/seerr/receipts", token.Secret, nil), http.StatusForbidden)
	if snapshot(t) != before {
		t.Fatal("reads changed ingress business state")
	}
	var counts [6]int
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT (SELECT count(*) FROM media_request),
  (SELECT count(*) FROM seerr_ingress_receipt),(SELECT count(*) FROM subscription),(SELECT count(*) FROM download_task),
  (SELECT count(*) FROM audit_log WHERE action='seerr.request.received'),
  (SELECT count(*) FROM audit_log WHERE action='media.request.submitted')`).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5]); err != nil || counts != [6]int{4, 4, 0, 0, 4, 4} {
		t.Fatal("unexpected requests, receipts, subscriptions, downloads or duplicate audits", counts, err)
	}
	var audit string
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(payload)::text,'') FROM audit_log`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{secret, privateEmail, privateURL, "requestedBy_email", "requestedBy_role"} {
		if strings.Contains(audit, private) {
			t.Fatal("audit persisted private Seerr input")
		}
	}
	if metadata.count() != calls {
		t.Fatal("pending ingress fetched metadata before local approval")
	}
	t.Log("SEERR_AUTO_APPROVED_HTTP_PG_PASS App.Run=REAL Cookie_config=REAL dedicated_no_cookie=REAL PG=18.6 events=MANUAL_AND_AUTO media=MOVIE_AND_EXPLICIT_TV pending=4 receipts=4 concurrent_retry=STABLE conflicts=409 rejected=NO_WRITES subscriptions=0 downloads=0 privacy=PASS")
}
