package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestSubscriptionSearchChecksOwnerAndStateAndNeverDownloadsUnverifiedCandidate(t *testing.T) {
	a, adminToken, memberToken, rootID, unitID, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	var mediaID, ownerID uuid.UUID
	if err := a.DBPool().QueryRow(ctx, "SELECT media_id FROM media_unit WHERE id = $1", unitID).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	if err := a.DBPool().QueryRow(ctx, "SELECT id FROM app_user WHERE username = 'admin'").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	subID := uuid.New()
	if _, err := a.DBPool().Exec(ctx, "INSERT INTO subscription (id, user_id, media_id, root_id) VALUES ($1, $2, $3, $4)", subID, ownerID, mediaID, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DBPool().Exec(ctx, "INSERT INTO wanted_item (id, subscription_id, unit_id, intent_version) VALUES ($1, $2, $3, 1)", uuid.New(), subID, unitID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/subscriptions/" + subID.String() + "/search"
	post := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(memberToken); rec.Code != http.StatusForbidden {
		t.Fatalf("another user's subscription: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := a.DBPool().Exec(ctx, "UPDATE subscription SET state = 'paused' WHERE id = $1", subID); err != nil {
		t.Fatal(err)
	}
	if rec := post(adminToken); rec.Code != http.StatusConflict {
		t.Fatalf("paused subscription: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := a.DBPool().Exec(ctx, "UPDATE subscription SET state = 'active' WHERE id = $1", subID); err != nil {
		t.Fatal(err)
	}
	rec := post(adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("active subscription: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		Matched     *bool      `json:"matched"`
		OperationID *uuid.UUID `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Matched == nil || *result.Matched || result.OperationID != nil {
		t.Fatalf("title-only search must not claim a validated match or download: %s", rec.Body.String())
	}
	var n int
	if err := a.DBPool().QueryRow(ctx, "SELECT count(*) FROM download_requirement").Scan(&n); err != nil || n != 0 {
		t.Fatalf("unverified candidate created a download reservation: count=%d err=%v", n, err)
	}
	// Simulate a broken search store in this disposable PG database. An error
	// must not masquerade as a successful search with no candidates.
	if _, err := a.DBPool().Exec(ctx, "DROP TABLE release_candidate CASCADE"); err != nil {
		t.Fatal(err)
	}
	if rec := post(adminToken); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("broken search store: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/releases/search?query=Fight", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("broken aggregate search: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
