package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperationStatusStorageFailureIsUnavailable(t *testing.T) {
	a, _, member, root, unit, candidate, router, _ := setupOperationHTTP(t)
	body, _ := json.Marshal(httpapi.AcquireMovieRequest{UnitId: unit, RootId: root, CandidateId: candidate})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/movies/acquire", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+member)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != 202 {
		t.Fatal(resp.Code, resp.Body.String())
	}
	var result httpapi.AcquireMovieResponse
	if e := json.Unmarshal(resp.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if _, e := a.DBPool().Exec(ctx, "ALTER TABLE transfer_plan RENAME TO fixture_unavailable_transfer_plan"); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := a.DBPool().Exec(ctx, "ALTER TABLE fixture_unavailable_transfer_plan RENAME TO transfer_plan"); e != nil {
			t.Error(e)
		}
	}()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+result.OperationId.String(), nil)
	req.Header.Set("Authorization", "Bearer "+member)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != 503 || !strings.Contains(resp.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatal("read failure must be safe RFC9457 503", resp.Code, resp.Body.String())
	}
	for _, secret := range []string{"fixture_unavailable", "transfer_plan", "SQLSTATE"} {
		if strings.Contains(resp.Body.String(), secret) {
			t.Fatal("underlying storage detail leaked")
		}
	}
}
