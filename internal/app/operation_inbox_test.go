package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	operation "github.com/dalamudx/PulseMedia/internal/usecase/operation"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestOperationInboxScopePagingAndPersistentState(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	admin, _, e := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen).SetupAdmin(ctx, "inbox-admin", "ActivityFixtureOwned123!")
	if e != nil {
		t.Fatal(e)
	}
	create := func(name string) *ports.Actor {
		u, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: name, PasswordHash: "unused", Role: ports.RoleMember, IsActive: true})
		if e != nil {
			t.Fatal(e)
		}
		return &ports.Actor{Type: ports.ActorTypeUser, ID: u.ID.String(), Role: u.Role}
	}
	member, other := create("inbox-member"), create("inbox-other")
	if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET actor_id=$2 WHERE operation_id=$1", f.op, member.ID); e != nil {
		t.Fatal(e)
	}
	owned := map[uuid.UUID]bool{f.op: true}
	foreign := map[uuid.UUID]bool{}
	var queuedOp, releasedOp uuid.UUID
	for i := 0; i < 5; i++ {
		op := uuid.New()
		if i == 0 {
			queuedOp = op
		}
		if i == 1 {
			releasedOp = op
		}
		who := member
		if i >= 3 {
			who = other
			foreign[op] = true
		} else {
			owned[op] = true
		}
		_, e = f.q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: op, RootID: f.root, UnitID: f.unit, ActorType: string(who.Type), ActorID: who.ID})
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: f.op, RootID: f.root, UnitID: f.unit, ActorType: string(other.Type), ActorID: other.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET created_at=$1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); e != nil {
		t.Fatal(e)
	}
	reader := postgres.NewOperationQueryStore(f.pool)
	svc := operation.NewService(reader)
	seen := map[uuid.UUID]bool{}
	cursor := ""
	pages := 0
	for {
		page, e := svc.List(ctx, member, ports.OperationListRequest{View: "all", Limit: 2, Cursor: cursor})
		if e != nil {
			t.Fatal(e)
		}
		pages++
		for _, v := range page.Items {
			if !owned[v.OperationID] || seen[v.OperationID] {
				t.Fatal("private or duplicate operation", v.OperationID)
			}
			seen[v.OperationID] = true
			if v.OperationID == f.op {
				single, e := svc.Read(ctx, member, f.op)
				if e != nil || single.Status != v.Status || v.DownloadStatus == nil {
					t.Fatal("inconsistent projected state", e)
				}
			}
		}
		if pages == 1 {
			if _, e = svc.List(ctx, other, ports.OperationListRequest{View: "all", Limit: 2, Cursor: page.NextCursor}); !errors.Is(e, ports.ErrInvalidOperationQuery) {
				t.Fatal("borrowed cursor", e)
			}
			if _, e = svc.List(ctx, member, ports.OperationListRequest{View: "review", Limit: 2, Cursor: page.NextCursor}); !errors.Is(e, ports.ErrInvalidOperationQuery) {
				t.Fatal("changed cursor view", e)
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		if pages > 3 {
			t.Fatal("unbounded paging")
		}
	}
	if len(seen) != 4 || pages != 2 {
		t.Fatal("paging", len(seen), pages)
	}
	readToken := *member
	readToken.Scopes = []string{"read"}
	if _, e = svc.List(ctx, &readToken, ports.OperationListRequest{}); e != nil {
		t.Fatal("read token", e)
	}
	adminActor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID.String(), Role: ports.RoleAdmin}
	all, e := svc.List(ctx, adminActor, ports.OperationListRequest{View: "all"})
	if e != nil || len(all.Items) != 6 {
		t.Fatal("admin scoped view", e)
	}
	raw, e := json.Marshal(all)
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"save_path", "payload", "actor_id", "password_encrypted", "candidate_id"} {
		if strings.Contains(string(raw), key) {
			t.Fatal("private storage data exposed", key)
		}
	}
	review, e := svc.List(ctx, member, ports.OperationListRequest{View: "review"})
	if e != nil || len(review.Items) != 3 {
		t.Fatal("review filter", e, len(review.Items))
	}
	for _, request := range []ports.OperationListRequest{{Limit: 51}, {View: "broken"}, {Cursor: "malformed"}} {
		if _, e = svc.List(ctx, member, request); !errors.Is(e, ports.ErrInvalidOperationQuery) {
			t.Fatal("invalid page", e)
		}
	}
	if _, e = f.pool.Exec(ctx, "INSERT INTO job(id,kind,state,payload) VALUES($1,'download_submission','queued',jsonb_build_object('operation_id',$2::text))", uuid.New(), queuedOp.String()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET state='released' WHERE operation_id=$1", releasedOp); e != nil {
		t.Fatal(e)
	}
	for op, expected := range map[uuid.UUID]string{queuedOp: "queued", releasedOp: "cancelled"} {
		single, e := svc.Read(ctx, member, op)
		if e != nil || single.Status != expected {
			t.Fatal("queue/withdrawn detail", e, single)
		}
		page, e := svc.List(ctx, member, ports.OperationListRequest{View: "all"})
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, row := range page.Items {
			if row.OperationID == op {
				found = true
				if row.Status != expected {
					t.Fatal("queue/withdrawn list", row)
				}
			}
		}
		if !found {
			t.Fatal("missing historical operation")
		}
	}
	review, e = svc.List(ctx, member, ports.OperationListRequest{View: "review"})
	if e != nil || len(review.Items) != 1 {
		t.Fatal("queue or released demand appeared as unknown", e, review)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET state='released' WHERE operation_id=$1", f.op); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE download_task SET state='unknown' WHERE operation_id=$1", f.op); e != nil {
		t.Fatal(e)
	}
	physical, e := svc.Read(ctx, member, f.op)
	if e != nil || physical.Status != "unknown" {
		t.Fatal("physical unknown hidden by released demand", e, physical)
	}
	uid := uuid.MustParse(member.ID)
	if _, e = f.pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uid); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.List(ctx, member, ports.OperationListRequest{}); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("inactive list", e)
	}
}
