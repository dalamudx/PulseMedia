package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
)

func TestOperationInboxActivityBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_INBOX_UI_FIXTURE", "1")
	runFullMoviePipelineSource(t, true, "root", "browser")
}
func activityInboxBrowserRouter(t *testing.T, ctx context.Context, f *organizationFixture, user, op uuid.UUID, hub *httpapi.SSEHub, next http.Handler) http.Handler {
	t.Helper()
	if _, e := f.pool.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", user); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 27; i++ {
		_, e := f.q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: uuid.New(), RootID: f.root, UnitID: f.unit, ActorType: string(ports.ActorTypeUser), ActorID: user.String()})
		if e != nil {
			t.Fatal(e)
		}
	}
	foreign, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "private-inbox-foreign", PasswordHash: "unused", Role: ports.RoleMember, IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	other := uuid.New()
	if _, e = f.q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: other, RootID: f.root, UnitID: f.unit, ActorType: string(ports.ActorTypeUser), ActorID: foreign.ID.String()}); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PULSE_INBOX_FOREIGN_OPERATION", other.String())
	var changed atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture_inbox_change" {
			if changed.CompareAndSwap(false, true) {
				if _, e := f.pool.Exec(ctx, "UPDATE download_task SET organization_state='review_required' WHERE operation_id=$1", op); e != nil {
					t.Error(e)
					w.WriteHeader(500)
					return
				}
			}
			hub.Broadcast("job.progress", "PRIVATE-FOREIGN-DATA")
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture_inbox_deactivate" {
			if _, e := f.pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", user); e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			hub.InvalidateActivities()
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
