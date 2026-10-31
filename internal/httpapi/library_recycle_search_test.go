package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recycleSearchHTTPFake struct {
	LibraryMaintenanceApplication
	query ports.LibraryRecycleQuery
	calls int
}

func (f *recycleSearchHTTPFake) ListRecycle(_ context.Context, _ *ports.Actor, q ports.LibraryRecycleQuery) (*ports.LibraryRecycleList, error) {
	f.query = q
	f.calls++
	return &ports.LibraryRecycleList{Items: []ports.LibraryRecycleItem{}}, nil
}
func TestLibraryMaintenanceRecycleSearchHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, query, body, key string
		status                 int
	}{
		{name: "filters", query: "?kind=tv&q=%20Show%25_%20&limit=1", status: 200},
		{name: "unknown", query: "?anything=1", status: 400},
		{name: "duplicate", query: "?q=a&q=b", status: 400},
		{name: "duplicate kind", query: "?kind=tv&kind=movie", status: 400},
		{name: "kind", query: "?kind=all", status: 400},
		{name: "limit", query: "?limit=0", status: 400},
		{name: "bad encoding", query: "?q=%zz", status: 400},
		{name: "invalid utf8", query: "?q=%ff", status: 400},
		{name: "nul", query: "?q=%00", status: 400},
		{name: "long query", query: "?q=" + strings.Repeat("a", 129), status: 400},
		{name: "long cursor", query: "?cursor=" + strings.Repeat("a", 2049), status: 400},
		{name: "body", body: "{}", status: 400},
		{name: "command key", key: "read-only", status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &recycleSearchHTTPFake{}
			mux := http.NewServeMux()
			RegisterLibraryMaintenanceRoutes(mux, fake)
			req := httptest.NewRequest("GET", "http://owned.test/api/v1/library-maintenance/recycle"+tc.query, strings.NewReader(tc.body))
			req = req.WithContext(context.WithValue(req.Context(), actorKey, untrackedScopeAdmin()))
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status == 200 {
				if fake.calls != 1 || fake.query.Kind != "tv" || fake.query.Query != "Show%_" || fake.query.Limit != 1 {
					t.Fatal(fake)
				}
			} else if fake.calls != 0 {
				t.Fatal("invalid request reached repository")
			}
		})
	}
	for _, actor := range []*ports.Actor{nil, {Type: ports.ActorTypeUser, Role: "member"}, {Type: "api_token", Role: ports.RoleAdmin}} {
		fake := &recycleSearchHTTPFake{}
		mux := http.NewServeMux()
		RegisterLibraryMaintenanceRoutes(mux, fake)
		req := httptest.NewRequest("GET", "/api/v1/library-maintenance/recycle", nil)
		if actor != nil {
			req = req.WithContext(context.WithValue(req.Context(), actorKey, actor))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if (w.Code != 401 && w.Code != 403) || fake.calls != 0 {
			t.Fatalf("unauthorized %d", w.Code)
		}
	}
}
