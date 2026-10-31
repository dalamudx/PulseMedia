package postgres

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func (t *accountManagementTx) RevokeOtherSessions(ctx context.Context) error {
	return db.New(t.tx).DeleteOtherManagedSessions(ctx, db.DeleteOtherManagedSessionsParams{UserID: t.user, KeepSessionID: t.session})
}
