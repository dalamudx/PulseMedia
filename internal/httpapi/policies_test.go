package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestPolicyCommandsAndPreview(t *testing.T) {
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	pool := a.DBPool()
	call := func(token, method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	list := call(admin, "GET", "/api/v1/policies", "", nil)
	var policies httpapi.ListPolicies200JSONResponse
	if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &policies) != nil || len(policies.Items) == 0 {
		t.Fatal(list.Code)
	}
	rules := policies.Items[0].Rules
	create := httpapi.CreatePolicyRequest{Id: "pg-policy", Name: "Policy fixture", Rules: rules}
	if r := call(member, "POST", "/api/v1/policies", "create", create); r.Code != 403 {
		t.Fatal("member create", r.Code)
	}
	if r := call(admin, "POST", "/api/v1/policies", "", create); r.Code != 400 {
		t.Fatal("missing key", r.Code)
	}
	if r := call(admin, "POST", "/api/v1/policies", "create", create); r.Code != 201 {
		t.Fatal("create", r.Code, r.Body.String())
	}
	if r := call(admin, "POST", "/api/v1/policies", "create", create); r.Code != 201 {
		t.Fatal("stable create replay", r.Code)
	}
	if r := call(admin, "POST", "/api/v1/policies", "different-create", create); r.Code != 409 {
		t.Fatal("duplicate ID", r.Code)
	}
	// Trigger failure at the last idempotency write rolls back revision/pointer/events/audit.
	if _, e := pool.Exec(ctx, "CREATE FUNCTION reject_policy_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='publish-once' THEN RAISE EXCEPTION 'fixture-secret'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_policy_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_policy_receipt()"); e != nil {
		t.Fatal(e)
	}
	rules.RejectTerms = []string{"forbidden"}
	publish := httpapi.PublishPolicyRequest{ExpectedRevision: 1, Rules: &rules}
	if r := call(admin, "POST", "/api/v1/policies/pg-policy/revisions", "publish-once", publish); r.Code != 503 || bytes.Contains(r.Body.Bytes(), []byte("fixture-secret")) {
		t.Fatal("partial or leaking policy failure", r.Code)
	}
	var revisions int
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM policy_revision WHERE policy_id='pg-policy'").Scan(&revisions); e != nil || revisions != 1 {
		t.Fatal("revision leaked on receipt failure", revisions, e)
	}
	if _, e := pool.Exec(ctx, "DROP TRIGGER reject_policy_receipt ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	results := make(chan *httptest.ResponseRecorder, 10)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- call(admin, "POST", "/api/v1/policies/pg-policy/revisions", "publish-once", publish)
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		var receipt httpapi.PolicyRevisionReceipt
		if r.Code != 201 || json.Unmarshal(r.Body.Bytes(), &receipt) != nil || receipt.Revision != 2 {
			t.Fatal("unstable publish", r.Code)
		}
	}
	if r := call(admin, "POST", "/api/v1/policies/pg-policy/revisions", "stale", publish); r.Code != 409 {
		t.Fatal("stale revision", r.Code)
	}
	publish.ExpectedRevision = 2
	if r := call(admin, "POST", "/api/v1/policies/pg-policy/revisions", "publish-once", publish); r.Code != 409 {
		t.Fatal("changed fingerprint", r.Code)
	}
	if r := call(member, "POST", "/api/v1/policies/pg-policy/revisions", "publish-once", publish); r.Code != 403 {
		t.Fatal("member replay elevated", r.Code)
	}
	if _, e := pool.Exec(ctx, "UPDATE policy_revision SET rules='{}' WHERE policy_id='pg-policy' AND revision=1"); e == nil {
		t.Fatal("revision mutable")
	}
	if _, e := pool.Exec(ctx, "DELETE FROM policy_revision WHERE policy_id='pg-policy' AND revision=1"); e == nil {
		t.Fatal("revision deletable")
	}
	preview := map[string]any{"policy_id": "pg-policy", "release_title": "Movie.2020.1080p.BluRay.forbidden"}
	r := call(admin, "POST", "/api/v1/policies/preview", "", preview)
	var result httpapi.PolicyPreviewResponse
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || result.Passed {
		t.Fatal("current preview ignored new rejection", r.Code, result)
	}
	preview["revision"] = 1
	r = call(admin, "POST", "/api/v1/policies/preview", "", preview)
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || !result.Passed {
		t.Fatal("frozen preview unavailable", r.Code, result)
	}
	preview["policy_id"] = "missing"
	if r = call(admin, "POST", "/api/v1/policies/preview", "", preview); r.Code != 404 {
		t.Fatal("fallback preview fabricated", r.Code)
	}
	if r = call(admin, "POST", "/api/v1/policies/preview", "", map[string]any{"rules": rules, "release_title": "Movie"}); r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || result.Passed {
		t.Fatal("unknown preview passed", r.Code, result)
	}
	restore := int64(1)
	if r = call(admin, "POST", "/api/v1/policies/pg-policy/revisions", "restore", httpapi.PublishPolicyRequest{ExpectedRevision: 2, RestoreRevision: &restore}); r.Code != 201 {
		t.Fatal("restore failed", r.Code)
	}
	r = call(admin, "GET", "/api/v1/policies/pg-policy/revisions/3", "", nil)
	var restored httpapi.PolicyDocument
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &restored) != nil || restored.Revision != 3 || len(restored.Rules.RejectTerms) != len(create.Rules.RejectTerms) {
		t.Fatal("restore did not copy old contents into new revision", r.Code)
	}
	var audits, events int
	if e := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_log WHERE target_type='policy' AND target_id='pg-policy'),(SELECT count(*) FROM outbox_event WHERE aggregate_type='policy' AND aggregate_id='pg-policy')").Scan(&audits, &events); e != nil || audits != 3 || events != 3 {
		t.Fatal("nonatomic audit/event/replay", audits, events, e)
	}
	_ = http.StatusOK
}
