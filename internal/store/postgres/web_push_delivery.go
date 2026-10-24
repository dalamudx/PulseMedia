package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WebPushDeliveryStore struct{ pool *pgxpool.Pool }

func NewWebPushDeliveryStore(pool *pgxpool.Pool) *WebPushDeliveryStore {
	return &WebPushDeliveryStore{pool}
}

type webPushExecution struct {
	connection *pgxpool.Conn
	device     uuid.UUID
}

func (s *WebPushDeliveryStore) OpenWebPushExecution(ctx context.Context, device uuid.UUID) (ports.WebPushDeliveryExecution, error) {
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, webPushError(ctx, err)
	}
	ok, err := db.New(conn).TryWebPushPhysicalLock(ctx, device.String())
	if err != nil || !ok {
		conn.Release()
		if err != nil {
			return nil, webPushError(ctx, err)
		}
		return nil, ports.ErrUnavailable
	}
	return &webPushExecution{conn, device}, nil
}
func (e *webPushExecution) Close() error {
	if e.connection == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, err := db.New(e.connection).ReleaseWebPushPhysicalLock(ctx, e.device.String())
	if err != nil || !ok {
		_ = e.connection.Conn().Close(ctx)
	}
	e.connection.Release()
	e.connection = nil
	if err != nil || !ok {
		return ports.ErrUnavailable
	}
	return nil
}
func (e *webPushExecution) guard(ctx context.Context, tx pgx.Tx, id uuid.UUID, owner string, token int64) (*db.Queries, *db.WebPushDeliveryBinding, error) {
	q := db.New(tx)
	if _, err := q.LockOutboxDelivery(ctx, id); err != nil {
		return nil, nil, ports.ErrStaleDeliveryLease
	}
	if _, err := q.GuardOutboxDelivery(ctx, db.GuardOutboxDeliveryParams{ID: id, Consumer: "webpush:" + e.device.String(), Owner: pgtype.Text{String: owner, Valid: true}, Token: token}); err != nil {
		return nil, nil, ports.ErrStaleDeliveryLease
	}
	binding, err := q.LoadWebPushDeliveryBinding(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if binding.DeviceID != e.device {
		return nil, nil, ports.ErrStaleDeliveryLease
	}
	return q, &db.WebPushDeliveryBinding{DeliveryID: binding.DeliveryID, DeviceID: binding.DeviceID, UserID: binding.UserID, VapidVersion: binding.VapidVersion, UnitID: binding.UnitID, FileID: binding.FileID, RootID: binding.RootID}, nil
}
func (e *webPushExecution) Load(ctx context.Context, id uuid.UUID, owner string, token int64, version string) (result *ports.WebPushOwnedDelivery, err error) {
	defer func() {
		if !errors.Is(err, ports.ErrStaleDeliveryLease) {
			err = webPushError(ctx, err)
		}
	}()
	tx, err := e.connection.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q, _, err := e.guard(ctx, tx, id, owner, token)
	if err != nil {
		return nil, err
	}
	if _, err = q.GuardWebPushDeliverySource(ctx, db.GuardWebPushDeliverySourceParams{DeliveryID: id, VapidVersion: version}); errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	delivery, err := q.GetOutboxDeliveryByID(ctx, id)
	if err != nil {
		return nil, err
	}
	binding, err := q.LoadWebPushDeliveryBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	event, err := q.GetOutboxEventByID(ctx, delivery.EventID)
	if err != nil {
		return nil, err
	}
	if event.EventType != "media.imported" || event.AggregateID != binding.UnitID.String() {
		return nil, ports.ErrForbidden
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ports.WebPushOwnedDelivery{EventID: event.ID, Ciphertext: append([]byte(nil), binding.SubscriptionEncrypted...)}, nil
}
func (e *webPushExecution) Complete(ctx context.Context, id uuid.UUID, owner string, token int64, outcome string) (err error) {
	defer func() {
		if !errors.Is(err, ports.ErrStaleDeliveryLease) {
			err = webPushError(ctx, err)
		}
	}()
	tx, err := e.connection.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q, binding, err := e.guard(ctx, tx, id, owner, token)
	if err != nil {
		return err
	}
	allowed := true
	if _, err = q.GuardWebPushDeliverySource(ctx, db.GuardWebPushDeliverySourceParams{DeliveryID: id, VapidVersion: binding.VapidVersion}); errors.Is(err, pgx.ErrNoRows) {
		allowed = false
	} else if err != nil {
		return err
	}
	if outcome == "expired" {
		if err = q.ExpireWebPushDevice(ctx, e.device); err != nil {
			return err
		}
		allowed = false
	}
	if outcome == "skipped" {
		allowed = false
	}
	if allowed {
		_, err = q.FinishOutboxDelivery(ctx, db.FinishOutboxDeliveryParams{Column1: id, Column2: owner, Column3: token})
	} else {
		_, err = q.SkipOwnedWebPushDelivery(ctx, db.SkipOwnedWebPushDeliveryParams{ID: id, Consumer: "webpush:" + e.device.String(), LeaseOwner: pgtype.Text{String: owner, Valid: true}, LeaseToken: token})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrStaleDeliveryLease
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
