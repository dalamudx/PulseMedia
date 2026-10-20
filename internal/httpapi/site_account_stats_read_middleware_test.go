package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestSiteAccountStatsReadRouteBoundary(t *testing.T) {
	for _, path := range []string{statsConfigPath, statsConfigPath + "/commands/key", statsReadPath + "/other", "/api/v1/indexers/" + statsConfigSource + "/other/account-stats"} {
		called := false
		h := strictSiteAccountStatsReadJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(204) }))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if !called || w.Code != 204 || w.Header().Get("Cache-Control") != "" {
			t.Fatal(path, w.Code, w.Header())
		}
	}
	for _, path := range []string{statsReadPath, "/api/v1/indexers/not-a-uuid/account-stats"} {
		h := siteAccountStatsReadNoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(path, w.Code, w.Header())
		}
	}
}

func TestSiteAccountStatsReadSubjectBeforeUnavailable(t *testing.T) {
	for _, actor := range []*ports.Actor{
		nil,
		{Type: ports.ActorTypeUser, ID: statsConfigAdmin().ID, Role: ports.RoleMember},
		{Type: ports.ActorTypeUser, ID: statsConfigAdmin().ID, Role: ports.RoleAdmin, Scopes: []string{}},
		{Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleAdmin},
	} {
		ctx := context.Background()
		status := 401
		if actor != nil {
			ctx = context.WithValue(ctx, actorKey, actor)
			status = 403
		}
		response, err := (&ServerHandler{}).ReadSiteAccountStats(ctx, ReadSiteAccountStatsRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		if err = response.VisitReadSiteAccountStatsResponse(w); err != nil || w.Code != status {
			t.Fatal(err, w.Code, w.Body.String())
		}
	}
}
