package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"slices"
)

// Match subscription list authority, then prohibit writes for the rest of this snapshot.
func lockSubscriptionReadAccount(ctx context.Context, q *db.Queries, a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) || a.Scopes != nil && !slices.Contains(a.Scopes, "read") {
		return ports.ErrForbidden
	}
	u, e := q.LockSubscriptionListAccount(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !u.IsActive || u.Role != a.Role {
		return ports.ErrForbidden
	}
	return q.SetSubscriptionListReadOnly(ctx)
}
