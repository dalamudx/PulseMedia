package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *MediaConfirmationStore) AcquireMediaRefreshPermit(ctx context.Context, id uuid.UUID) (func(), error) {
	return acquireMediaServerPermit(ctx, s.pool, id)
}
func (s *MediaConfirmationStore) LoadMediaRefresh(ctx context.Context, id, server uuid.UUID, owner string, token int64) (*ports.MediaRefreshInput, error) {
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	delivery, e := q.GuardOutboxDelivery(ctx, db.GuardOutboxDeliveryParams{ID: id, Consumer: "mediaserver.refresh:" + server.String(), Owner: pgtype.Text{String: owner, Valid: true}, Token: token})
	if e != nil {
		return nil, ports.ErrStaleDeliveryLease
	}
	event, e := q.GetOutboxEventByID(ctx, delivery.EventID)
	if e != nil {
		return nil, e
	}
	var payload struct {
		FileID uuid.UUID `json:"file_id"`
		UnitID uuid.UUID `json:"unit_id"`
	}
	if event.EventType != "media.imported" || json.Unmarshal(event.Payload, &payload) != nil || payload.UnitID.String() != event.AggregateID {
		return nil, ports.ErrBadResponse
	}
	input, e := confirmationSnapshot(ctx, q, server, payload.FileID, payload.UnitID, false)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &ports.MediaRefreshInput{Confirmation: *input, DeliveryID: id, Owner: owner, Token: token}, nil
}
func (s *MediaConfirmationStore) validateRefresh(ctx context.Context, in ports.MediaRefreshInput, finish bool) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	input := in.Confirmation
	if _, e = q.LockMediaConfirmationServer(ctx, input.Server.ID); e != nil {
		return ports.ErrUnavailable
	}
	if _, e = q.LockTransferRoot(ctx, input.RootID); e != nil {
		return e
	}
	if _, e = q.LockTransferUnit(ctx, input.UnitID); e != nil {
		return e
	}
	if _, e = q.LockTransferOldFile(ctx, input.FileID); e != nil {
		return e
	}
	if _, e = q.LockOutboxDelivery(ctx, in.DeliveryID); e != nil {
		return ports.ErrStaleDeliveryLease
	}
	current, e := confirmationSnapshot(ctx, q, input.Server.ID, input.FileID, input.UnitID, false)
	if e != nil {
		return e
	}
	if !sameMediaSnapshot(*current, input) {
		return ports.ErrUnavailable
	}
	delivery, e := q.GuardOutboxDelivery(ctx, db.GuardOutboxDeliveryParams{ID: in.DeliveryID, Consumer: "mediaserver.refresh:" + input.Server.ID.String(), Owner: pgtype.Text{String: in.Owner, Valid: true}, Token: in.Token})
	if e != nil {
		return ports.ErrStaleDeliveryLease
	}
	event, e := q.GetOutboxEventByID(ctx, delivery.EventID)
	if e != nil {
		return e
	}
	var payload struct {
		FileID uuid.UUID `json:"file_id"`
		UnitID uuid.UUID `json:"unit_id"`
	}
	if event.EventType != "media.imported" || event.AggregateID != input.UnitID.String() || json.Unmarshal(event.Payload, &payload) != nil || payload.FileID != input.FileID || payload.UnitID != input.UnitID {
		return ports.ErrBadResponse
	}
	if finish {
		if _, e = q.FinishOutboxDelivery(ctx, db.FinishOutboxDeliveryParams{Column1: in.DeliveryID, Column2: in.Owner, Column3: in.Token}); e != nil {
			return ports.ErrStaleDeliveryLease
		}
	}
	return tx.Commit(ctx)
}
func (s *MediaConfirmationStore) ValidateMediaRefresh(ctx context.Context, in ports.MediaRefreshInput) error {
	return s.validateRefresh(ctx, in, false)
}
func (s *MediaConfirmationStore) FinishMediaRefresh(ctx context.Context, in ports.MediaRefreshInput) error {
	return s.validateRefresh(ctx, in, true)
}

var _ ports.MediaRefreshRepository = (*MediaConfirmationStore)(nil)
