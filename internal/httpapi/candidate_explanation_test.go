package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/google/uuid"
	"net/http/httptest"
	"testing"
)

func TestCandidateExplanationHTTPObjectScopeEmptyAndReadFailure(t *testing.T) {
	a, _, member, root, unit, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	u, e := a.Queries().GetSearchUnit(ctx, unit)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(httpapi.CreateSubscriptionRequest{MediaId: &u.MediaID, RootId: &root})
	create := httptest.NewRequest("POST", "/api/v1/subscriptions", bytes.NewReader(raw))
	create.Header.Set("Authorization", "Bearer "+member)
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Idempotency-Key", uuid.NewString())
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	var sub httpapi.SubscriptionDetails
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &sub) != nil || sub.WantedItems == nil || len(*sub.WantedItems) != 1 {
		t.Fatal("create", created.Code)
	}
	wanted := (*sub.WantedItems)[0].Id
	path := "/api/v1/subscriptions/" + sub.Id.String() + "/wanted/" + wanted.String() + "/candidates"
	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+member)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	r := call(path)
	var report httpapi.CandidateExplanationReport
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &report) != nil || len(report.Items) != 0 || report.IntentVersion != int64(sub.IntentVersion) {
		t.Fatal("empty result invented source", r.Code, report)
	}
	if r.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("candidate response cached", r.Header())
	}
	wrong := call("/api/v1/subscriptions/" + sub.Id.String() + "/wanted/" + uuid.NewString() + "/candidates")
	if wrong.Code != 404 {
		t.Fatal("foreign wanted accepted", wrong.Code)
	}
	if _, e = a.DBPool().Exec(ctx, "ALTER TABLE candidate_rule_cache RENAME TO unavailable_candidate_rule_cache; ALTER TABLE search_round RENAME TO unavailable_search_round"); e != nil {
		t.Fatal(e)
	}
	failed := call(path)
	if failed.Code != 503 || bytes.Contains(failed.Body.Bytes(), []byte("unavailable_search_round")) {
		t.Fatal("read fault returned empty success/raw sql", failed.Code)
	}
}
