package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WebPushStore struct {
	pool   *pgxpool.Pool
	ids    ports.IDGen
	outbox notificationTestPublisher
}

func NewWebPushStore(pool *pgxpool.Pool, ids ports.IDGen, ob notificationTestPublisher) *WebPushStore {
	return &WebPushStore{pool, ids, ob}
}

type webPushReceipt struct {
	Version string              `json:"version"`
	Device  ports.WebPushDevice `json:"device"`
}

func webPushError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, known := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrWebPushConflict} {
		if errors.Is(err, known) {
			return known
		}
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ports.ErrWebPushConflict
	}
	return ports.ErrUnavailable
}
func (s *WebPushStore) begin(ctx context.Context, a *ports.Actor) (pgx.Tx, *db.Queries, error) {
	if err := acquisitionCommandActor(a); err != nil {
		return nil, nil, err
	}
	if s == nil || s.pool == nil {
		return nil, nil, ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := db.New(tx)
	if err = lockAcquisitionUser(ctx, q, a); err != nil {
		managementRollback(ctx, tx)
		return nil, nil, err
	}
	return tx, q, nil
}
func decodeWebPushReceipt(old db.IdempotencyRecord) (*ports.WebPushDevice, error) {
	var r webPushReceipt
	if json.Unmarshal(old.ResponseBody, &r) != nil || r.Version != "webpush.device.v1" || r.Device.ID == uuid.Nil || !old.ResourceID.Valid || old.ResourceID.String != r.Device.ID.String() || r.Device.CreatedAt.IsZero() || r.Device.UpdatedAt.Before(r.Device.CreatedAt) || (r.Device.Status != "active" && r.Device.Status != "revoked") {
		return nil, ports.ErrWebPushConflict
	}
	return &r.Device, nil
}
func (s *WebPushStore) RegisterWebPush(ctx context.Context, a *ports.Actor, id uuid.UUID, key, hash string, prepare ports.WebPushRegistrationPrepare) (*ports.WebPushDevice, error) {
	return s.command(ctx, a, id, time.Time{}, key, hash, prepare)
}
func (s *WebPushStore) RevokeWebPush(ctx context.Context, a *ports.Actor, id uuid.UUID, expected time.Time, key, hash string) (*ports.WebPushDevice, error) {
	return s.command(ctx, a, id, expected, key, hash, nil)
}
func (s *WebPushStore) command(ctx context.Context, a *ports.Actor, id uuid.UUID, expected time.Time, key, hash string, prepare ports.WebPushRegistrationPrepare) (result *ports.WebPushDevice, err error) {
	defer func() { err = webPushError(ctx, err) }()
	if id == uuid.Nil || key == "" || len(key) > 160 || hash == "" || (prepare == nil && expected.IsZero()) {
		return nil, ports.ErrWebPushConflict
	}
	if s == nil || s.ids == nil || s.outbox == nil {
		return nil, ports.ErrUnavailable
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	// Account-wide serialization covers concurrent quota checks as well as same-key saves.
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "webpush.devices", AggregateID: a.ID}); err != nil {
		return nil, err
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		if old.RequestHash != hash {
			return nil, ports.ErrWebPushConflict
		}
		result, e = decodeWebPushReceipt(old)
		if e != nil || result.ID != id {
			return nil, ports.ErrWebPushConflict
		}
		return result, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	userID := uuid.MustParse(a.ID)
	action := "webpush.device.registered"
	code := int32(201)
	if prepare != nil {
		count, e := q.CountActiveWebPushDevices(ctx, userID)
		if e != nil {
			return nil, e
		}
		if count >= 10 {
			return nil, ports.ErrWebPushConflict
		}
		input, e := prepare()
		if e != nil {
			return nil, e
		}
		if input.ID != id || len(input.VAPIDVersion) != 64 || len(input.EndpointDigest) != 64 || len(input.Ciphertext) < 40 {
			return nil, ports.ErrWebPushConflict
		}
		row, e := q.InsertWebPushDevice(ctx, db.InsertWebPushDeviceParams{ID: id, UserID: userID, VapidVersion: input.VAPIDVersion, EndpointDigest: input.EndpointDigest, SubscriptionEncrypted: input.Ciphertext})
		if e != nil {
			return nil, e
		}
		result = &ports.WebPushDevice{ID: row.ID, Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	} else {
		row, e := q.LockWebPushDevice(ctx, id)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		if row.UserID != userID {
			return nil, ports.ErrNotFound
		}
		if row.Status != "active" || !row.UpdatedAt.Time.Equal(expected) {
			return nil, ports.ErrWebPushConflict
		}
		updated, e := q.RevokeWebPushDevice(ctx, db.RevokeWebPushDeviceParams{ID: id, UserID: userID, UpdatedAt: pgtype.Timestamptz{Time: expected, Valid: true}})
		if e != nil {
			return nil, e
		}
		result = &ports.WebPushDevice{ID: updated.ID, Status: updated.Status, CreatedAt: updated.CreatedAt.Time, UpdatedAt: updated.UpdatedAt.Time}
		action = "webpush.device.revoked"
		code = 200
	}
	if _, err = s.outbox.Publish(ctx, q, "webpush.device", id.String(), action, map[string]string{"status": result.Status}, nil); err != nil {
		return nil, err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: action, TargetType: "web_push_device", TargetID: id.String(), Payload: []byte("{}")}); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(webPushReceipt{Version: "webpush.device.v1", Device: *result})
	if _, err = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: code, ResponseBody: raw, ResourceID: pgtype.Text{String: id.String(), Valid: true}}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *WebPushStore) ListWebPush(ctx context.Context, a *ports.Actor) (result []ports.WebPushDevice, err error) {
	defer func() { err = webPushError(ctx, err) }()
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	rows, err := q.ListWebPushDevices(ctx, uuid.MustParse(a.ID))
	if err != nil {
		return nil, err
	}
	result = make([]ports.WebPushDevice, 0, len(rows))
	for _, r := range rows {
		result = append(result, ports.WebPushDevice{ID: r.ID, Status: r.Status, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time})
	}
	return result, nil
}
func (s *WebPushStore) GetWebPushReceipt(ctx context.Context, a *ports.Actor, key string) (result *ports.WebPushDevice, err error) {
	defer func() { err = webPushError(ctx, err) }()
	if key == "" || len(key) > 160 {
		return nil, ports.ErrWebPushConflict
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	old, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeWebPushReceipt(old)
}
