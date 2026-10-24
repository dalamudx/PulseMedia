package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/command"
	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const seedingConfigAction = "seeding.configuration"

type SeedingConfigStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewSeedingConfigStore(pool *pgxpool.Pool, ids ports.IDGen) *SeedingConfigStore {
	return &SeedingConfigStore{pool: pool, ids: ids}
}

var _ ports.SeedingConfigRepository = (*SeedingConfigStore)(nil)

func seedingConfigError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrRejected, ports.ErrSeedingConfigConflict} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}
func seedingConfigKey(key string) bool {
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
func seedingConfigDigest(digest string) bool {
	if len(digest) != 64 || digest != strings.ToLower(digest) {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}
func seedingConfigView(row db.SeedingConfiguration) (ports.SeedingConfigView, error) {
	out := ports.SeedingConfigView{ClientID: row.ClientID, OwnerID: row.OwnerID, Version: row.Version, ClientUpdatedAt: row.ClientUpdatedAt.Time.UTC(), Enabled: row.Enabled, IntervalSeconds: row.IntervalSeconds, Rule: rule.Rule{Mode: rule.MatchMode(row.MatchMode), DeleteFiles: row.DeleteFiles}, UpdatedAt: row.UpdatedAt.Time.UTC()}
	if row.MinSeedingSeconds.Valid {
		n := row.MinSeedingSeconds.Int64
		out.Rule.MinSeedingSeconds = &n
	}
	if row.MinShareRatio.Valid {
		n := row.MinShareRatio.Float64
		out.Rule.MinShareRatio = &n
	}
	if row.LowFreeBytes.Valid {
		n := row.LowFreeBytes.Int64
		out.Rule.LowFreeBytes = &n
	}
	if out.ClientID == uuid.Nil || out.OwnerID == uuid.Nil || out.Version < 1 || !row.ClientUpdatedAt.Valid || out.ClientUpdatedAt.IsZero() || !row.CreatedAt.Valid || row.CreatedAt.Time.IsZero() || !row.UpdatedAt.Valid || out.UpdatedAt.IsZero() || row.UpdatedAt.Time.Before(row.CreatedAt.Time) || !seedingConfigDigest(row.ClientStamp) || out.IntervalSeconds < 60 || out.IntervalSeconds > 86400 || out.Rule.Validate() != nil {
		return ports.SeedingConfigView{}, ports.ErrUnavailable
	}
	return out, nil
}
func seedingReceiptValid(r *ports.SeedingConfigReceipt, a *ports.Actor, client uuid.UUID, key string) bool {
	return r != nil && r.CommandKey == key && r.Config.ClientID == client && r.Config.OwnerID.String() == a.ID && r.Config.Version > 0 && !r.Config.ClientUpdatedAt.IsZero() && !r.Config.UpdatedAt.IsZero() && r.Config.IntervalSeconds >= 60 && r.Config.IntervalSeconds <= 86400 && r.Config.Rule.Validate() == nil
}
func seedingOriginalKey(client uuid.UUID, key string) string {
	return seedingConfigAction + ":" + client.String() + ":" + key
}
func seedingOriginal(ctx context.Context, q *db.Queries, a *ports.Actor, client uuid.UUID, key string, digest *string) (*ports.SeedingConfigReceipt, error) {
	row, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: seedingOriginalKey(client, key)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if digest != nil && row.RequestHash != *digest {
		return nil, ports.ErrSeedingConfigConflict
	}
	if !seedingConfigDigest(row.RequestHash) || row.ResponseCode != 200 || !row.ResourceID.Valid || row.ResourceID.String != client.String() {
		return nil, ports.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(row.ResponseBody))
	decoder.DisallowUnknownFields()
	var receipt ports.SeedingConfigReceipt
	if decoder.Decode(&receipt) != nil {
		return nil, ports.ErrUnavailable
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) || !seedingReceiptValid(&receipt, a, client, key) {
		return nil, ports.ErrUnavailable
	}
	receipt.Config.ClientUpdatedAt = receipt.Config.ClientUpdatedAt.UTC()
	receipt.Config.UpdatedAt = receipt.Config.UpdatedAt.UTC()
	return &receipt, nil
}
func (s *SeedingConfigStore) begin(ctx context.Context, a *ports.Actor) (pgx.Tx, *db.Queries, error) {
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
func (s *SeedingConfigStore) ReadSeedingConfig(ctx context.Context, a *ports.Actor, client uuid.UUID) (out *ports.SeedingConfigView, err error) {
	defer func() { err = seedingConfigError(err) }()
	if client == uuid.Nil {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	row, err := q.GetSeedingConfig(ctx, client)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err = q.GetDownloadClientByID(ctx, client); errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		} else if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	view, err := seedingConfigView(row)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &view, nil
}
func (s *SeedingConfigStore) ReadSeedingConfigReceipt(ctx context.Context, a *ports.Actor, client uuid.UUID, key string, digest *string) (out *ports.SeedingConfigReceipt, err error) {
	defer func() { err = seedingConfigError(err) }()
	if client == uuid.Nil || !seedingConfigKey(key) || digest != nil && !seedingConfigDigest(*digest) {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	out, err = seedingOriginal(ctx, q, a, client, key, digest)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func seedingInt(n *int64) pgtype.Int8 {
	if n == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *n, Valid: true}
}
func seedingFloat(n *float64) pgtype.Float8 {
	if n == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *n, Valid: true}
}
func (s *SeedingConfigStore) SaveSeedingConfig(ctx context.Context, a *ports.Actor, client uuid.UUID, in ports.SeedingConfigInput, key, digest string) (out *ports.SeedingConfigReceipt, err error) {
	defer func() { err = seedingConfigError(err) }()
	if client == uuid.Nil || !seedingConfigKey(key) || !seedingConfigDigest(digest) {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	lock, _ := json.Marshal([]string{a.ID, client.String(), key})
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: seedingConfigAction + ".command", AggregateID: string(lock)}); err != nil {
		return nil, err
	}
	if out, err = seedingOriginal(ctx, q, a, client, key, &digest); err != nil {
		return nil, err
	} else if out != nil {
		return out, tx.Commit(ctx)
	}
	if in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || in.ExpectedClientUpdatedAt.IsZero() || in.IntervalSeconds < 60 || in.IntervalSeconds > 86400 || in.Rule.Validate() != nil {
		return nil, ports.ErrRejected
	}
	aggregate := db.LockOutboxAggregateParams{AggregateType: seedingConfigAction, AggregateID: client.String()}
	if err = q.LockOutboxAggregate(ctx, aggregate); err != nil {
		return nil, err
	}
	endpoint, err := q.LockDownloadClientForEdit(ctx, client)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !endpoint.UpdatedAt.Valid || !endpoint.UpdatedAt.Time.Equal(in.ExpectedClientUpdatedAt) {
		return nil, ports.ErrSeedingConfigConflict
	}
	if endpoint.ClientType != "qbittorrent" && endpoint.ClientType != "transmission" {
		return nil, ports.ErrRejected
	}
	stamp, err := q.DownloadGlobalRatesBinding(ctx, client)
	if err != nil {
		return nil, err
	}
	if !seedingConfigDigest(stamp) {
		return nil, ports.ErrUnavailable
	}
	previous, err := q.LockSeedingConfig(ctx, client)
	absent := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !absent {
		return nil, err
	}
	if absent && in.ExpectedVersion != 0 || !absent && previous.Version != in.ExpectedVersion {
		return nil, ports.ErrSeedingConfigConflict
	}
	owner := uuid.MustParse(a.ID)
	clientTime := pgtype.Timestamptz{Time: in.ExpectedClientUpdatedAt.UTC(), Valid: true}
	var saved db.SeedingConfiguration
	if absent {
		saved, err = q.CreateSeedingConfig(ctx, db.CreateSeedingConfigParams{ClientID: client, OwnerID: owner, ClientUpdatedAt: clientTime, ClientStamp: stamp, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, MatchMode: string(in.Rule.Mode), MinSeedingSeconds: seedingInt(in.Rule.MinSeedingSeconds), MinShareRatio: seedingFloat(in.Rule.MinShareRatio), LowFreeBytes: seedingInt(in.Rule.LowFreeBytes)})
	} else {
		saved, err = q.UpdateSeedingConfig(ctx, db.UpdateSeedingConfigParams{ClientID: client, OwnerID: owner, ExpectedVersion: in.ExpectedVersion, ClientUpdatedAt: clientTime, ClientStamp: stamp, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, MatchMode: string(in.Rule.Mode), MinSeedingSeconds: seedingInt(in.Rule.MinSeedingSeconds), MinShareRatio: seedingFloat(in.Rule.MinShareRatio), LowFreeBytes: seedingInt(in.Rule.LowFreeBytes)})
	}
	if err != nil {
		return nil, err
	}
	view, err := seedingConfigView(saved)
	if err != nil {
		return nil, err
	}
	out = &ports.SeedingConfigReceipt{CommandKey: key, Config: view}
	safe, err := json.Marshal(map[string]any{"client_id": client, "owner_id": owner, "version": saved.Version, "client_updated_at": view.ClientUpdatedAt, "enabled": saved.Enabled, "interval_seconds": saved.IntervalSeconds, "rule": view.Rule})
	if err != nil {
		return nil, err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: seedingConfigAction, TargetType: "download_client", TargetID: client.String(), Payload: safe}); err != nil {
		return nil, err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams(aggregate))
	if err != nil {
		return nil, err
	}
	if _, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: seedingConfigAction, AggregateID: client.String(), EventType: "seeding.configuration.updated", Seq: int64(seq), Payload: safe}); err != nil {
		return nil, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, err = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: seedingOriginalKey(client, key), RequestHash: digest, ResponseCode: 200, ResponseBody: body, ResourceID: pgtype.Text{String: client.String(), Valid: true}}); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return nil, err
	}
	if err = settingUser(ctx, q, a, true); err != nil {
		return nil, err
	}
	current, err := q.GetSeedingConfig(ctx, client)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current, saved) {
		return nil, ports.ErrSeedingConfigConflict
	}
	endpointAfter, err := q.GetDownloadClientByID(ctx, client)
	if err != nil {
		return nil, err
	}
	stampAfter, err := q.DownloadGlobalRatesBinding(ctx, client)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(endpointAfter, endpoint) || stampAfter != stamp {
		return nil, ports.ErrSeedingConfigConflict
	}
	restored, err := seedingOriginal(ctx, q, a, client, key, &digest)
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
