package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyTransferApprovalCannotBypassExecutionBoundary(t *testing.T) {
	a, admin, member, root, _, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	plan, err := a.Queries().CreateTransferPlan(ctx, db.CreateTransferPlanParams{ID: uuid.New(), OperationID: uuid.New(), RootID: root, State: "completed", RequiresApproval: true, Strategy: "hardlink"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token string
		status      int
	}{{"admin", admin, 409}, {"member", member, 403}, {"anonymous", "", 401}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/transfer-plans/"+plan.ID.String()+"/approve", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("legacy approval returned %d instead of %d: %s", out.Code, tc.status, out.Body.String())
			}
			if tc.status == 409 {
				var body map[string]any
				if json.Unmarshal(out.Body.Bytes(), &body) != nil || body["code"] != "PLAN_APPROVAL_REQUIRES_REVIEW" {
					t.Fatal("legacy approval contract", out.Body.String())
				}
			}
		})
	}
	got, err := a.Queries().GetTransferPlanByID(ctx, plan.ID)
	if err != nil || got != plan {
		t.Fatal("legacy route changed original plan", err)
	}
}
