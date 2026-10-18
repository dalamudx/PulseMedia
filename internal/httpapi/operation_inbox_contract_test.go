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
	"time"
)

func TestOperationInboxPublicErrorsAndScope(t *testing.T) {
	a, _, member, root, unit, candidate, router, hub := setupOperationHTTP(t)
	body, _ := json.Marshal(httpapi.AcquireMovieRequest{UnitId: unit, RootId: root, CandidateId: candidate})
	req := httptest.NewRequest("POST", "/api/v1/movies/acquire", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+member)
	req.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	router.ServeHTTP(created, req)
	if created.Code != 202 {
		t.Fatal("native acquire", created.Code, created.Body.String())
	}
	read := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+member)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	page := read("/api/v1/operations?view=review&limit=25")
	if page.Code != 200 || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("scoped page", page.Code, page.Body.String())
	}
	for _, path := range []string{"/api/v1/operations?limit=0", "/api/v1/operations?limit=51", "/api/v1/operations?cursor=bad!", "/api/v1/operations?view=other"} {
		r := read(path)
		if r.Code != 400 {
			t.Fatal("invalid page", path, r.Code, r.Body.String())
		}
	}
	hub.BroadcastLog("PRIVATE-INBOX-LOG")
	streamCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	streamReq := httptest.NewRequest("GET", "/api/v1/stream", nil).WithContext(streamCtx)
	streamReq.Header.Set("Authorization", "Bearer "+member)
	streamReq.Header.Set("Last-Event-ID", "previous-instance:9")
	stream := httptest.NewRecorder()
	router.ServeHTTP(stream, streamReq)
	cancel()
	if stream.Code != 200 || stream.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(stream.Body.String(), "event: reset") || strings.Contains(stream.Body.String(), "PRIVATE-INBOX-LOG") {
		t.Fatal("generated scoped stream", stream.Code, stream.Body.String())
	}
	ctx := context.Background()
	if _, e := a.DBPool().Exec(ctx, "ALTER TABLE transfer_plan RENAME TO fixture_inbox_unavailable_plan"); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := a.DBPool().Exec(ctx, "ALTER TABLE fixture_inbox_unavailable_plan RENAME TO transfer_plan"); e != nil {
			t.Error(e)
		}
	}()
	broken := read("/api/v1/operations")
	if broken.Code != http.StatusServiceUnavailable || !strings.Contains(broken.Header().Get("Content-Type"), "application/problem+json") || strings.Contains(broken.Body.String(), "fixture_inbox_unavailable_plan") || strings.Contains(broken.Body.String(), "SQLSTATE") {
		t.Fatal("unsafe unavailable", broken.Code, broken.Body.String())
	}
}
