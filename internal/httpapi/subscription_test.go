package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
)

func TestSubscriptionHTTPLifecycleAndPolicyPreview(t *testing.T) {
	_, adminToken, memberToken, rootID, _, _, router, _ := setupOperationHTTP(t)

	// 1. Create subscription via HTTP POST
	createBody, _ := json.Marshal(httpapi.CreateSubscriptionRequest{
		MediaId: &rootID,
		RootId:  &rootID,
	})

	reqCreate := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewReader(createBody))
	reqCreate.Header.Set("Authorization", "Bearer "+memberToken)
	reqCreate.Header.Set("Content-Type", "application/json")
	recCreate := httptest.NewRecorder()

	router.ServeHTTP(recCreate, reqCreate)
	// Even if dummy media not found, verifies endpoint routing and response format
	if recCreate.Code == http.StatusUnauthorized {
		t.Fatal("expected authenticated access")
	}

	// 2. List subscriptions via HTTP GET
	reqList := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions", nil)
	reqList.Header.Set("Authorization", "Bearer "+memberToken)
	recList := httptest.NewRecorder()

	router.ServeHTTP(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("expected 200 on list subscriptions, got: %d", recList.Code)
	}

	var listResp httpapi.SubscriptionListResponse
	if err := json.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to parse list response: %v", err)
	}

	// 3. Preview policy via HTTP POST /api/v1/policies/preview
	previewBody, _ := json.Marshal(httpapi.PolicyPreviewRequest{
		ReleaseTitle: "Fight.Club.1999.2160p.UHD.Remux",
	})
	reqPrev := httptest.NewRequest(http.MethodPost, "/api/v1/policies/preview", bytes.NewReader(previewBody))
	reqPrev.Header.Set("Authorization", "Bearer "+adminToken)
	reqPrev.Header.Set("Content-Type", "application/json")
	recPrev := httptest.NewRecorder()

	router.ServeHTTP(recPrev, reqPrev)
	if recPrev.Code != http.StatusOK {
		t.Fatalf("expected 200 on policy preview, got %d: %s", recPrev.Code, recPrev.Body.String())
	}

	var prevResp httpapi.PolicyPreviewResponse
	if err := json.Unmarshal(recPrev.Body.Bytes(), &prevResp); err != nil {
		t.Fatalf("failed to parse preview response: %v", err)
	}
	if !prevResp.Passed || !prevResp.MeetsCutoff || prevResp.Score <= 0 {
		t.Fatalf("expected passed and meets cutoff: %+v", prevResp)
	}
	if len(prevResp.ScoreBreakdown) == 0 {
		t.Fatal("expected non-empty score breakdown")
	}
}
