package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
)

func (t *accountManagementTx) ReadCommand(ctx context.Context, key string) (*ports.SecurityCommandReceipt, error) {
	if key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidAccountCommand
	}
	r, e := db.New(t.tx).ReadAccountSecurityCommand(ctx, db.ReadAccountSecurityCommandParams{OwnerID: t.user, CommandKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if (r.Action == "account.created" || r.Action == "account.updated" || r.Action == "account.member_password.reset") && t.account.User.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	return &ports.SecurityCommandReceipt{Action: r.Action, Result: r.Response}, nil
}
