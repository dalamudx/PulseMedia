package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

func m9WordHTTPState(t *testing.T, f *m5IndexerAcceptance) map[string][32]byte {
	t.Helper()
	p := f.app.DBPool()
	rows, err := p.Query(t.Context(), `SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name NOT IN ('app_user','app_session') ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][32]byte, len(names))
	for _, name := range names {
		var value string
		if err = p.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM `+pgx.Identifier{"public", name}.Sanitize()+` r`).Scan(&value); err != nil {
			t.Fatal("protected table snapshot", name, err)
		}
		result[name] = sha256.Sum256([]byte(value))
	}
	return result
}

func m9WordHTTPStable(t *testing.T, f *m5IndexerAcceptance) map[string][32]byte {
	t.Helper()
	var previous map[string][32]byte
	stableSince := time.Now()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var active int
		if err := f.app.DBPool().QueryRow(t.Context(), `SELECT count(*) FROM job WHERE state NOT IN ('succeeded','dead','cancelled','failed','expired')`).Scan(&active); err != nil {
			t.Fatal(err)
		}
		current := m9WordHTTPState(t, f)
		if active == 0 && reflect.DeepEqual(previous, current) {
			if time.Since(stableSince) >= 1200*time.Millisecond {
				return current
			}
		} else {
			stableSince = time.Now()
		}
		previous = current
		if time.Now().After(deadline) {
			t.Fatal("initial App work did not settle before read-only attribution")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func m9WordHTTPDefault(t *testing.T, f *m5IndexerAcceptance, session string) httpapi.PolicyRuleSet {
	t.Helper()
	response := f.request(t, http.MethodGet, "/api/v1/policies", "", nil, session)
	m5IndexerStatus(t, response, http.StatusOK)
	var list httpapi.ListPolicies200JSONResponse
	if json.Unmarshal(response.body, &list) != nil {
		t.Fatal("policy list encoding")
	}
	for _, doc := range list.Items {
		if doc.Id == "default" {
			return doc.Rules
		}
	}
	t.Fatal("default policy missing")
	return httpapi.PolicyRuleSet{}
}

func TestM9WordPolicyCookieHTTPPG(t *testing.T) {
	f, metadata := newM5CreationAcceptance(t)
	rules := m9WordHTTPDefault(t, f, f.session)
	rules.CutoffScore = 400
	words := []httpapi.QualityWordRule{{Word: "Atmos", Score: 200}, {Word: "1080p", Required: true}, {Word: "CAM", Excluded: true}}
	rules.CustomWords = &words
	create := f.request(t, http.MethodPost, "/api/v1/policies", "word-http-create", httpapi.CreatePolicyRequest{Id: "word-http", Name: "Word HTTP", Rules: rules}, f.session)
	m5IndexerStatus(t, create, http.StatusCreated)
	base := m9WordHTTPStable(t, f)
	metadataBefore := metadata.count()
	replay := f.request(t, http.MethodPost, "/api/v1/policies", "word-http-create", httpapi.CreatePolicyRequest{Id: "word-http", Name: "Word HTTP", Rules: rules}, f.session)
	m5IndexerStatus(t, replay, http.StatusCreated)
	if !bytes.Equal(create.body, replay.body) {
		t.Fatal("create replay changed original receipt bytes")
	}
	read := f.request(t, http.MethodGet, "/api/v1/policies/word-http/revisions/1", "", nil, f.session)
	m5IndexerStatus(t, read, http.StatusOK)
	var document httpapi.PolicyDocument
	if json.Unmarshal(read.body, &document) != nil || document.Rules.CustomWords == nil || !reflect.DeepEqual(*document.Rules.CustomWords, words) {
		t.Fatal("HTTP word rule round trip lost fields")
	}
	for _, tc := range []struct {
		title  string
		passed bool
	}{
		{"Movie.2020.1080p.BluRay.Atmos.x264-GROUP", true},
		{"Movie.2020.1080p.BluRay.CAM.Atmos.x264-GROUP", false},
		{"Movie.2020.720p.BluRay.Atmos.x264-GROUP", false},
	} {
		response := f.request(t, http.MethodPost, "/api/v1/policies/preview", "", map[string]any{"policy_id": "word-http", "revision": 1, "release_title": tc.title}, f.session)
		m5IndexerStatus(t, response, http.StatusOK)
		var preview httpapi.PolicyPreviewResponse
		if json.Unmarshal(response.body, &preview) != nil || preview.Passed != tc.passed {
			t.Fatal("public word admission mismatch")
		}
		if tc.passed && (preview.Score != 340 || preview.PreferenceScore == nil || *preview.PreferenceScore != 200 || preview.MeetsCutoff) {
			t.Fatal("word preference changed public quality or cutoff")
		}
	}
	if !reflect.DeepEqual(base, m9WordHTTPState(t, f)) || metadata.count() != metadataBefore {
		t.Fatal("GET, preview or replay wrote protected rows or queried metadata")
	}
	invalid := [][]httpapi.QualityWordRule{
		{{Word: "", Score: 1}}, {{Word: " DV", Score: 1}}, {{Word: "DV", Score: 1001}},
		{{Word: "DV", Required: true, Excluded: true}}, {{Word: "CAM", Excluded: true, Score: 1}},
		{{Word: "DV", Score: 1}, {Word: "dv", Required: true}}, {{Word: "DV"}},
	}
	for i, words := range invalid {
		rejectedRules := rules
		rejectedRules.CustomWords = &words
		response := f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", fmt.Sprintf("word-http-invalid-%d", i), httpapi.PublishPolicyRequest{ExpectedRevision: 1, Rules: &rejectedRules}, f.session)
		m5IndexerStatus(t, response, http.StatusBadRequest)
	}
	raw, _ := json.Marshal(httpapi.PublishPolicyRequest{ExpectedRevision: 1, Rules: &rules})
	duplicate := strings.Replace(string(raw), `"word":"Atmos"`, `"word":"Atmos","word":"DV"`, 1)
	response := f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", "word-http-duplicate-json", duplicate, f.session)
	m5IndexerStatus(t, response, http.StatusBadRequest)
	unknown := strings.Replace(string(raw), `"word":"Atmos"`, `"word":"Atmos","execute":true`, 1)
	response = f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", "word-http-unknown-json", unknown, f.session)
	m5IndexerStatus(t, response, http.StatusBadRequest)
	if !reflect.DeepEqual(base, m9WordHTTPState(t, f)) {
		t.Fatal("rejected declaration changed protected rows")
	}
	replacement := rules
	replacementWords := []httpapi.QualityWordRule{{Word: "Atmos", Excluded: true}}
	replacement.CustomWords = &replacementWords
	publishBody := httpapi.PublishPolicyRequest{ExpectedRevision: 1, Rules: &replacement}
	published := f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", "word-http-publish", publishBody, f.session)
	m5IndexerStatus(t, published, http.StatusCreated)
	restore := int64(1)
	response = f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", "word-http-restore", httpapi.PublishPolicyRequest{ExpectedRevision: 2, RestoreRevision: &restore}, f.session)
	m5IndexerStatus(t, response, http.StatusCreated)
	base = m9WordHTTPStable(t, f)
	replay = f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", "word-http-publish", publishBody, f.session)
	m5IndexerStatus(t, replay, http.StatusCreated)
	if !bytes.Equal(published.body, replay.body) || !reflect.DeepEqual(base, m9WordHTTPState(t, f)) {
		t.Fatal("old policy command replay published another version")
	}
	read = f.request(t, http.MethodGet, "/api/v1/policies/word-http/revisions/3", "", nil, f.session)
	m5IndexerStatus(t, read, http.StatusOK)
	if json.Unmarshal(read.body, &document) != nil || document.Rules.CustomWords == nil || !reflect.DeepEqual(*document.Rules.CustomWords, words) {
		t.Fatal("restore did not reproduce immutable word rules")
	}
	empty := []httpapi.QualityWordRule{}
	cleared := rules
	cleared.CustomWords = &empty
	response = f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", "word-http-clear", httpapi.PublishPolicyRequest{ExpectedRevision: 3, Rules: &cleared}, f.session)
	m5IndexerStatus(t, response, http.StatusCreated)
	base = m9WordHTTPStable(t, f)
	read = f.request(t, http.MethodGet, "/api/v1/policies/word-http/revisions/4", "", nil, f.session)
	m5IndexerStatus(t, read, http.StatusOK)
	document = httpapi.PolicyDocument{}
	if json.Unmarshal(read.body, &document) != nil || document.Rules.CustomWords != nil || bytes.Contains(read.body, []byte(`"custom_words"`)) {
		t.Fatal("explicit empty rules did not restore legacy shape")
	}
	response = f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", "word-http-anonymous", publishBody, "")
	m5IndexerStatus(t, response, http.StatusUnauthorized)
	if _, err := f.app.DBPool().Exec(t.Context(), `UPDATE app_user SET role='member' WHERE username='creation-admin'`); err != nil {
		t.Fatal(err)
	}
	response = f.request(t, http.MethodPost, "/api/v1/policies/word-http/revisions", "word-http-publish", publishBody, f.session)
	m5IndexerStatus(t, response, http.StatusForbidden)
	if !reflect.DeepEqual(base, m9WordHTTPState(t, f)) || metadata.count() != metadataBefore {
		t.Fatal("denied replay or read changed business state or requested metadata")
	}
	if _, err := f.app.DBPool().Exec(t.Context(), `UPDATE app_user SET role='admin' WHERE username='creation-admin'`); err != nil {
		t.Fatal(err)
	}
	t.Log("M9_WORD_POLICY_COOKIE_HTTP_PASS Cookie=REAL App=REAL PG=18.6 strict_rules=true quality_cutoff=UNCHANGED preferences=SEPARATE immutable_restore=true original_receipts=BYTE_IDENTICAL readonly_rows=FULL_EXCEPT_AUTH metadata_calls=0")
}

func TestM9WordPolicyAutomaticTVNativeHTTPPG(t *testing.T) {
	h := newM5AutomaticTVAcceptance(t, false)
	adminSession := m5CreationLogin(t, h.f, "creation-admin")
	policyID, ok := h.body["quality_policy_id"].(string)
	if !ok || policyID == "" {
		t.Fatal("native TV fixture must bind an explicit policy")
	}
	read := h.f.request(t, http.MethodGet, "/api/v1/policies/"+policyID+"/revisions/1", "", nil, adminSession)
	m5IndexerStatus(t, read, http.StatusOK)
	var document httpapi.PolicyDocument
	if json.Unmarshal(read.body, &document) != nil || document.Id != policyID || document.Revision != 1 {
		t.Fatal("bound policy read failed")
	}
	rules := document.Rules
	words := []httpapi.QualityWordRule{{Word: "WEB-DL", Required: true, Score: -1000}}
	rules.CustomWords = &words
	published := h.f.request(t, http.MethodPost, "/api/v1/policies/"+policyID+"/revisions", "word-tv-native-policy", httpapi.PublishPolicyRequest{ExpectedRevision: document.Revision, Rules: &rules}, adminSession)
	m5IndexerStatus(t, published, http.StatusCreated)
	receipt := h.create(t)
	op := h.awaitAdd(t)
	if h.count(t, `SELECT count(*) FROM subscription WHERE id=$1 AND quality_policy_id=$2 AND quality_policy_revision=2`, h.subscription, policyID) != 1 {
		t.Fatal("native TV did not freeze published word revision")
	}
	h.complete.Store(true)
	h.unblockAdd()
	h.awaitImported(t, op, 100*time.Second)
	h.assertImported(t, op, receipt)
	var score int
	if err := h.f.app.DBPool().QueryRow(context.Background(), `SELECT (rules->>'cutoff_score')::integer FROM policy_revision WHERE policy_id=$1 AND revision=2`, policyID).Scan(&score); err != nil || score != rules.CutoffScore {
		t.Fatal("preference changed saved cutoff")
	}
	t.Log("M9_WORD_POLICY_NATIVE_TV_PASS Cookie=REAL policy_revision=2 required_word=true negative_preference=-1000 intrinsic_authority_score=UNCHANGED original_bytes=true Add=1 imported=1 same_inode=true original_key=true")
}
