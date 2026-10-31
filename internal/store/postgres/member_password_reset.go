package postgres

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (t *accountManagementTx) ResetMemberPassword(ctx context.Context, target uuid.UUID, hash string) (ports.User, error) {
	if !t.adminBoundary || t.account.User.Role != ports.RoleAdmin || target == t.user {
		return ports.User{}, ports.ErrForbidden
	}
	q := db.New(t.tx)
	u, e := q.LockAccountForSecurity(ctx, target)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.User{}, ports.ErrNotFound
	}
	if e != nil {
		return ports.User{}, e
	}
	if u.Role != ports.RoleMember {
		return ports.User{}, ports.ErrForbidden
	}
	targetMFA := &mfaTransaction{tx: t.tx, user: target}
	state, e := targetMFA.LoadMFA(ctx)
	if e != nil {
		return ports.User{}, e
	}
	if !state.Enabled {
		if e = targetMFA.DeleteMFA(ctx); e != nil {
			return ports.User{}, e
		}
	}
	if e = q.ChangeManagedPassword(ctx, db.ChangeManagedPasswordParams{ID: target, PasswordHash: hash}); e != nil {
		return ports.User{}, e
	}
	if e = t.revokeCredentials(ctx, target, uuid.Nil); e != nil {
		return ports.User{}, e
	}
	return authenticationUser(u), nil
}
