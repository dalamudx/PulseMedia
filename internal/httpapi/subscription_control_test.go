package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/google/uuid"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSubscriptionControlAtomicReceiptScopeAndVersions(t *testing.T) {
	a, _, token, root, unit, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	call := func(method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	u, e := a.Queries().GetSearchUnit(ctx, unit)
	if e != nil {
		t.Fatal(e)
	}
	created := call("POST", "/api/v1/subscriptions", "control-fixture-create", httpapi.CreateSubscriptionRequest{MediaId: &u.MediaID, RootId: &root})
	var sub httpapi.SubscriptionDetails
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &sub) != nil {
		t.Fatal(created.Code)
	}
	path := "/api/v1/subscriptions/" + sub.Id.String() + "/control"
	body := map[string]any{"action": "state", "state": "paused", "expected_version": sub.IntentVersion}
	if _, e = a.DBPool().Exec(ctx, "CREATE FUNCTION reject_control_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='pause-once' THEN RAISE EXCEPTION 'fixture-secret';END IF;RETURN NEW;END $$; CREATE TRIGGER reject_control_result BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_control_result()"); e != nil {
		t.Fatal(e)
	}
	beforeAudit := 0
	beforeEvent := 0
	if e = a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event)").Scan(&beforeAudit, &beforeEvent); e != nil {
		t.Fatal(e)
	}
	failed := call("PUT", path, "pause-once", body)
	if failed.Code != 503 || bytes.Contains(failed.Body.Bytes(), []byte("fixture-secret")) {
		t.Fatal(failed.Code)
	}
	saved, e := a.Queries().GetSubscriptionByID(ctx, sub.Id)
	if e != nil || saved.State != "active" || saved.IntentVersion != int64(sub.IntentVersion) {
		t.Fatal("receipt fault partially committed control", saved, e)
	}
	var audit, event int
	if e = a.DBPool().QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event)").Scan(&audit, &event); e != nil || audit != beforeAudit || event != beforeEvent {
		t.Fatal("partial audit/event", audit, event, e)
	}
	if _, e = a.DBPool().Exec(ctx, "DROP TRIGGER reject_control_result ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	outputs := make(chan *httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); outputs <- call("PUT", path, "pause-once", body) }()
	}
	wg.Wait()
	close(outputs)
	for output := range outputs {
		var receipt httpapi.SubscriptionControlReceipt
		if output.Code != 200 || json.Unmarshal(output.Body.Bytes(), &receipt) != nil || receipt.State != "paused" || receipt.IntentVersion != int64(sub.IntentVersion)+1 || output.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("unstable control receipt", output.Code, receipt)
		}
	}
	stale := call("PUT", path, "resume-stale", map[string]any{"action": "state", "state": "active", "expected_version": sub.IntentVersion})
	if stale.Code != 409 {
		t.Fatal("stale state request authorized", stale.Code)
	}
	wrong := call("PUT", path, "skip-foreign", map[string]any{"action": "skip", "unit_id": uuid.New(), "skipped": true, "expected_version": sub.IntentVersion + 1})
	if wrong.Code != 404 {
		t.Fatal("foreign unit entered target", wrong.Code)
	}
	skip := call("PUT", path, "skip-once", map[string]any{"action": "skip", "unit_id": unit, "skipped": true, "expected_version": sub.IntentVersion + 1})
	if skip.Code != 200 {
		t.Fatal(skip.Code)
	}
	items, e := a.Queries().ListWantedItemsForSubscription(ctx, sub.Id)
	if e != nil || len(items) != 1 || !items[0].IsSkipped || items[0].IntentVersion != int64(sub.IntentVersion)+2 {
		t.Fatal("skip version not propagated", items, e)
	}
	saved, e = a.Queries().GetSubscriptionByID(ctx, sub.Id)
	if e != nil || saved.State != "paused" {
		t.Fatal("skip resumed state", saved, e)
	}
	for _, old := range []struct{ method, path string }{{"POST", "/api/v1/subscriptions/" + sub.Id.String() + "/pause"}, {"POST", "/api/v1/subscriptions/" + sub.Id.String() + "/resume"}, {"DELETE", "/api/v1/subscriptions/" + sub.Id.String()}} {
		r := call(old.method, old.path, "", nil)
		if r.Code != 409 {
			t.Fatal("versionless command bypassed fence", old.method, r.Code)
		}
	}
	saved, e = a.Queries().GetSubscriptionByID(ctx, sub.Id)
	if e != nil || saved.State != "paused" || saved.IntentVersion != int64(sub.IntentVersion)+2 {
		t.Fatal("legacy control mutated current intent/history", saved, e)
	}
	if _, e = a.DBPool().Exec(ctx, "UPDATE subscription SET user_id=(SELECT id FROM app_user WHERE role='admin' LIMIT 1) WHERE id=$1", sub.Id); e != nil {
		t.Fatal(e)
	}
	replay := call("PUT", path, "pause-once", body)
	if replay.Code != 403 {
		t.Fatal("cached control elevated object scope", replay.Code)
	}
}
