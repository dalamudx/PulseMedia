package pgtest

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"testing"
)

// CreateUserActor creates a real current user for an explicit positive PG fixture.
// It does not alter existing users or authorize commands on their behalf.
func CreateUserActor(t *testing.T, q *db.Queries, a *ports.Actor) *ports.Actor {
	t.Helper()
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Scopes != nil {
		t.Fatal("invalid positive user fixture")
	}
	if _, e = q.CreateUser(context.Background(), db.CreateUserParams{ID: id, Username: "fixture-" + id.String(), PasswordHash: "fixture-no-login", Role: a.Role, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	return a
}
