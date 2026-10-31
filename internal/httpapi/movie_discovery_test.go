package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

type movieDiscoveryHTTPRepo struct {
	reads, schedules int
	status           *ports.MovieDiscoveryStatus
}

func (r *movieDiscoveryHTTPRepo) ListDueMovieDiscoveries(context.Context, int32) ([]uuid.UUID, error) {
	r.schedules++
	return nil, nil
}
func (r *movieDiscoveryHTTPRepo) ScheduleMovieDiscovery(context.Context, uuid.UUID) (bool, error) {
	r.schedules++
	return false, nil
}
func (r *movieDiscoveryHTTPRepo) ReadMovieDiscovery(context.Context, *ports.Actor, uuid.UUID) (*ports.MovieDiscoveryStatus, error) {
	r.reads++
	return r.status, nil
}

func TestMovieDiscoveryHTTPReadOnly(t *testing.T) {
	sub, search, file := uuid.New(), uuid.New(), uuid.New()
	repo := &movieDiscoveryHTTPRepo{status: &ports.MovieDiscoveryStatus{SubscriptionID: sub, SearchID: &search, CurrentFileID: &file, State: "succeeded"}}
	h := &ServerHandler{}
	h.SetMovieDiscoveryService(usecaseSearch.NewMovieDiscoveryService(repo))
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	response, e := h.GetMovieDiscovery(context.WithValue(t.Context(), actorKey, actor), GetMovieDiscoveryRequestObject{Id: sub})
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	if e = response.VisitGetMovieDiscoveryResponse(w); e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d headers=%v", w.Code, w.Header())
	}
	var got ports.MovieDiscoveryStatus
	if e = json.Unmarshal(w.Body.Bytes(), &got); e != nil || got.ExecutionAllowed || got.SearchID == nil || *got.SearchID != search {
		t.Fatalf("body %+v %v", got, e)
	}
	if repo.reads != 1 || repo.schedules != 0 {
		t.Fatalf("GET reads=%d schedules=%d", repo.reads, repo.schedules)
	}
	for _, tc := range []struct {
		name  string
		actor *ports.Actor
		want  int
	}{
		{"unauthenticated", nil, 401},
		{"member", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}, 403},
		{"token_scope", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin, Scopes: []string{"subscriptions:read"}}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.actor != nil {
				ctx = context.WithValue(ctx, actorKey, tc.actor)
			}
			r, e := h.GetMovieDiscovery(ctx, GetMovieDiscoveryRequestObject{Id: sub})
			if e != nil {
				t.Fatal(e)
			}
			w := httptest.NewRecorder()
			if e = r.VisitGetMovieDiscoveryResponse(w); e != nil {
				t.Fatal(e)
			}
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
		})
	}
	if repo.reads != 1 || repo.schedules != 0 {
		t.Fatal("denied GET reached repository or scheduler")
	}
}
