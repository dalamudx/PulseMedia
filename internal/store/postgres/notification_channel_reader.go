package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NotificationChannelReader exposes only the connection snapshot needed for delivery.
type NotificationChannelReader struct{ queries *db.Queries }

func NewNotificationChannelReader(pool *pgxpool.Pool) *NotificationChannelReader {
	return &NotificationChannelReader{queries: Queries(pool)}
}

func (r *NotificationChannelReader) LoadNotificationChannel(ctx context.Context, id uuid.UUID) (*ports.NotificationChannelConnection, error) {
	row, err := r.queries.GetNotificationChannelByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ports.NotificationChannelConnection{ID: row.ID, ChannelType: row.ChannelType, IsEnabled: row.IsEnabled, ConfigEncrypted: append([]byte(nil), row.ConfigEncrypted...)}, nil
}

var _ ports.NotificationChannelReader = (*NotificationChannelReader)(nil)
