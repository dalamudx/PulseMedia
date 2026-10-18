package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"testing"
)

func TestOperationSnapshotCurrentUserAndScope(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	who, _, e := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen).SetupAdmin(ctx, "operation-owner", "OperationFixtureOwned123!")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET actor_id=$2 WHERE operation_id=$1", f.op, who.ID.String()); e != nil {
		t.Fatal(e)
	}
	reader := postgres.NewOperationQueryStore(f.pool)
	owner := &ports.Actor{Type: ports.ActorTypeUser, ID: who.ID.String(), Role: ports.RoleAdmin}
	snapshot, e := reader.ReadOperation(ctx, owner, f.op)
	if e != nil || snapshot.OperationID != f.op || snapshot.Status != "tracking" || snapshot.DownloadStatus == nil {
		t.Fatal("real snapshot", snapshot, e)
	}
	for _, bad := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: owner.ID, Role: owner.Role}, {Type: ports.ActorTypeService, ID: owner.ID, Role: owner.Role}, {Type: ports.ActorTypeUser, ID: owner.ID, Role: ports.RoleMember}, {Type: ports.ActorTypeUser, ID: owner.ID, Role: owner.Role, Scopes: []string{"write"}}} {
		if _, e = reader.ReadOperation(ctx, bad, f.op); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("bad read credential", bad, e)
		}
	}
	read := *owner
	read.Scopes = []string{"read"}
	if _, e = reader.ReadOperation(ctx, &read, f.op); e != nil {
		t.Fatal("scoped read", e)
	}
	if _, e = reader.ReadOperation(ctx, owner, uuid.New()); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal("missing operation", e)
	}
	foreign, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "foreign-operation", PasswordHash: "unused-hash", Role: ports.RoleMember, IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reader.ReadOperation(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: foreign.ID.String(), Role: foreign.Role}, f.op); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("foreign read", e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", who.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = reader.ReadOperation(ctx, owner, f.op); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("disabled read", e)
	}
}
