package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"reflect"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/command"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CookieCloudConfigStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewCookieCloudConfigStore(pool *pgxpool.Pool, ids ports.IDGen) *CookieCloudConfigStore {
	return &CookieCloudConfigStore{pool: pool, ids: ids}
}

var _ ports.CookieCloudConfigRepository = (*CookieCloudConfigStore)(nil)

const cookieCloudConfigAction = "cookiecloud.configuration"

func cookieCloudConfigError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrRejected, ports.ErrNotFound, ports.ErrCookieCloudConfigConflict} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}
func cookieCloudCommandKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == ':', r == '.':
		default:
			return false
		}
	}
	return true
}
func cookieCloudDigest(hash string) bool {
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}
func cookieCloudOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == "" && len(origin) <= 2048 && origin == u.Scheme+"://"+strings.ToLower(u.Host)
}
func cookieCloudConfigView(row db.CookiecloudConfiguration) (ports.CookieCloudConfigView, error) {
	if row.OwnerID == uuid.Nil || row.Version < 1 || row.IntervalSeconds < 60 || row.IntervalSeconds > 86400 || !cookieCloudOrigin(row.ServerOrigin) || len(row.ConnectionEncrypted) == 0 || !row.CreatedAt.Valid || !row.UpdatedAt.Valid || row.UpdatedAt.Time.Before(row.CreatedAt.Time) {
		return ports.CookieCloudConfigView{}, ports.ErrUnavailable
	}
	return ports.CookieCloudConfigView{OwnerID: row.OwnerID, Version: row.Version, Enabled: row.Enabled, IntervalSeconds: row.IntervalSeconds, ServerOrigin: row.ServerOrigin, HasConnection: true, UpdatedAt: row.UpdatedAt.Time.UTC()}, nil
}
func cookieCloudReceiptValid(r *ports.CookieCloudConfigReceipt, actor *ports.Actor, key string) bool {
	return r != nil && r.CommandKey == key && r.Config.OwnerID.String() == actor.ID && r.Config.Version > 0 && r.Config.IntervalSeconds > 0 && r.Config.HasConnection && cookieCloudOrigin(r.Config.ServerOrigin) && !r.Config.UpdatedAt.IsZero()
}
func cookieCloudOriginal(ctx context.Context, q *db.Queries, a *ports.Actor, key string, digest *string) (*ports.CookieCloudConfigReceipt, error) {
	row, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: cookieCloudConfigAction + ":" + key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if digest != nil && row.RequestHash != *digest {
		return nil, ports.ErrCookieCloudConfigConflict
	}
	if !cookieCloudDigest(row.RequestHash) || row.ResponseCode != 200 || !row.ResourceID.Valid || row.ResourceID.String != a.ID {
		return nil, ports.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(row.ResponseBody))
	decoder.DisallowUnknownFields()
	var receipt ports.CookieCloudConfigReceipt
	if decoder.Decode(&receipt) != nil {
		return nil, ports.ErrUnavailable
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) || !cookieCloudReceiptValid(&receipt, a, key) {
		return nil, ports.ErrUnavailable
	}
	receipt.Config.UpdatedAt = receipt.Config.UpdatedAt.UTC()
	return &receipt, nil
}
func (s *CookieCloudConfigStore) begin(ctx context.Context, a *ports.Actor) (pgx.Tx, *db.Queries, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := command.AuthorizeSession(a, ports.RoleAdmin); err != nil {
		return nil, nil, err
	}
	if s == nil || s.pool == nil || s.ids == nil {
		return nil, nil, ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := db.New(tx)
	if err = settingUser(ctx, q, a, true); err != nil {
		managementRollback(ctx, tx)
		return nil, nil, err
	}
	return tx, q, nil
}
func (s *CookieCloudConfigStore) ReadCookieCloudConfig(ctx context.Context, a *ports.Actor) (out *ports.CookieCloudConfigView, err error) {
	defer func() { err = cookieCloudConfigError(err) }()
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	row, err := q.GetCookieCloudConfig(ctx, uuid.MustParse(a.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	view, err := cookieCloudConfigView(row)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &view, nil
}
func (s *CookieCloudConfigStore) ReadCookieCloudConfigReceipt(ctx context.Context, a *ports.Actor, key string, digest *string) (out *ports.CookieCloudConfigReceipt, err error) {
	defer func() { err = cookieCloudConfigError(err) }()
	if !cookieCloudCommandKey(key) || (digest != nil && !cookieCloudDigest(*digest)) {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	out, err = cookieCloudOriginal(ctx, q, a, key, digest)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *CookieCloudConfigStore) SaveCookieCloudConfig(ctx context.Context, a *ports.Actor, in ports.PreparedCookieCloudConfig, key, digest string) (out *ports.CookieCloudConfigReceipt, err error) {
	defer func() { err = cookieCloudConfigError(err) }()
	if !cookieCloudCommandKey(key) || !cookieCloudDigest(digest) {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	lock, _ := json.Marshal([]string{a.ID, key})
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: cookieCloudConfigAction + ".command", AggregateID: string(lock)}); err != nil {
		return nil, err
	}
	if out, err = cookieCloudOriginal(ctx, q, a, key, &digest); err != nil {
		return nil, err
	} else if out != nil {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return out, nil
	}
	if in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || in.IntervalSeconds < 60 || in.IntervalSeconds > 86400 {
		return nil, ports.ErrRejected
	}
	if in.PreserveConnection {
		if in.ExpectedVersion == 0 || len(in.ConnectionEncrypted) != 0 || in.ServerOrigin != "" {
			return nil, ports.ErrRejected
		}
	} else if len(in.ConnectionEncrypted) == 0 || len(in.ConnectionEncrypted) > 65536 || !cookieCloudOrigin(in.ServerOrigin) {
		return nil, ports.ErrRejected
	}
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: cookieCloudConfigAction, AggregateID: a.ID}); err != nil {
		return nil, err
	}
	owner := uuid.MustParse(a.ID)
	previous, err := q.LockCookieCloudConfig(ctx, owner)
	absent := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !absent {
		return nil, err
	}
	if absent && in.ExpectedVersion != 0 || !absent && previous.Version != in.ExpectedVersion {
		return nil, ports.ErrCookieCloudConfigConflict
	}
	encrypted, origin := in.ConnectionEncrypted, in.ServerOrigin
	if in.PreserveConnection {
		encrypted, origin = previous.ConnectionEncrypted, previous.ServerOrigin
	}
	var saved db.CookiecloudConfiguration
	if absent {
		saved, err = q.CreateCookieCloudConfig(ctx, db.CreateCookieCloudConfigParams{OwnerID: owner, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, ServerOrigin: origin, ConnectionEncrypted: encrypted})
	} else {
		saved, err = q.UpdateCookieCloudConfig(ctx, db.UpdateCookieCloudConfigParams{OwnerID: owner, ExpectedVersion: in.ExpectedVersion, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, ServerOrigin: origin, ConnectionEncrypted: encrypted})
	}
	if err != nil {
		return nil, err
	}
	view, err := cookieCloudConfigView(saved)
	if err != nil {
		return nil, err
	}
	out = &ports.CookieCloudConfigReceipt{CommandKey: key, Config: view}
	safe, _ := json.Marshal(map[string]any{"owner_id": owner, "version": saved.Version, "enabled": saved.Enabled, "interval_seconds": saved.IntervalSeconds, "connection_configured": true})
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: cookieCloudConfigAction, TargetType: "cookiecloud", TargetID: a.ID, Payload: safe}); err != nil {
		return nil, err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: cookieCloudConfigAction, AggregateID: a.ID})
	if err != nil {
		return nil, err
	}
	if _, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: cookieCloudConfigAction, AggregateID: a.ID, EventType: "cookiecloud.configuration.updated", Seq: int64(seq), Payload: safe}); err != nil {
		return nil, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, err = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: cookieCloudConfigAction + ":" + key, RequestHash: digest, ResponseCode: 200, ResponseBody: body, ResourceID: pgtype.Text{String: a.ID, Valid: true}}); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return nil, err
	}
	if err = settingUser(ctx, q, a, true); err != nil {
		return nil, err
	}
	current, err := q.GetCookieCloudConfig(ctx, owner)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current, saved) {
		return nil, ports.ErrCookieCloudConfigConflict
	}
	restored, err := cookieCloudOriginal(ctx, q, a, key, &digest)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(restored, out) {
		return nil, ports.ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
