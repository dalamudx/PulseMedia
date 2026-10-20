package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const doubanSyncNamespace = "douban.sync.config:"

type DoubanSyncConfigStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

var _ ports.DoubanSyncConfigRepository = (*DoubanSyncConfigStore)(nil)

func NewDoubanSyncConfigStore(pool *pgxpool.Pool, ids ports.IDGen) *DoubanSyncConfigStore {
	return &DoubanSyncConfigStore{pool: pool, ids: ids}
}

func doubanSyncCopyActor(actor *ports.Actor) (*ports.Actor, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	a := *actor
	// A non-nil scope slice denotes a Token, including an empty slice.
	if err := acquisitionCommandActor(&a); err != nil {
		return nil, err
	}
	return &a, nil
}
func doubanSyncCopyID(id *uuid.UUID) *uuid.UUID {
	if id == nil {
		return nil
	}
	v := *id
	return &v
}
func doubanSyncASCII(s string, max int, uid bool) bool {
	if len(s) < 1 || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if uid && i == 0 {
			return false
		}
		if c == '_' || c == '.' || c == '-' || (!uid && c == ':') {
			continue
		}
		return false
	}
	return true
}
func doubanSyncValid(in ports.DoubanSyncInput) bool {
	return doubanSyncASCII(in.DoubanUID, 64, true) && in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64 &&
		(in.MovieRootID == nil || *in.MovieRootID != uuid.Nil) && (in.TVRootID == nil || *in.TVRootID != uuid.Nil) &&
		(!in.Enabled || in.MovieRootID != nil || in.TVRootID != nil)
}
func doubanSyncHash(in ports.DoubanSyncInput) string {
	raw, _ := json.Marshal(struct {
		Namespace string                `json:"namespace"`
		Input     ports.DoubanSyncInput `json:"input"`
	}{doubanSyncNamespace, in})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func doubanSyncError(err error) error {
	for _, known := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrRejected, ports.ErrNotFound, ports.ErrSubscriptionBindingsConflict} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}
func doubanSyncNullable(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}
func doubanSyncID(id pgtype.UUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := uuid.UUID(id.Bytes)
	return &v
}
func doubanSyncConfig(row db.DoubanSyncConfig) ports.DoubanSyncConfig {
	return ports.DoubanSyncConfig{ID: row.ID, OwnerID: row.OwnerID, DoubanUID: row.DoubanUid, Enabled: row.Enabled, MovieRootID: doubanSyncID(row.MovieRootID), TVRootID: doubanSyncID(row.TvRootID), Version: row.Version, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
}

// Owner locking serializes creation and version changes, including absent configs.
func (s *DoubanSyncConfigStore) lockOwner(ctx context.Context, tx pgx.Tx, a *ports.Actor) (*db.Queries, error) {
	q := db.New(tx)
	u, err := q.LockDoubanSyncOwner(ctx, uuid.MustParse(a.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	if !u.IsActive || u.Role != a.Role {
		return nil, ports.ErrForbidden
	}
	return q, nil
}
func (s *DoubanSyncConfigStore) ReadDoubanSyncConfig(ctx context.Context, actor *ports.Actor) (*ports.DoubanSyncConfig, error) {
	a, err := doubanSyncCopyActor(actor)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q, err := s.lockOwner(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDoubanSyncConfig(ctx, uuid.MustParse(a.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	c := doubanSyncConfig(row)
	return &c, nil
}

func doubanSyncSameID(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func doubanSyncReceipt(ctx context.Context, q *db.Queries, owner uuid.UUID, key string) (*ports.DoubanSyncConfigReceipt, string, error) {
	row, err := q.GetDoubanSyncReceipt(ctx, db.GetDoubanSyncReceiptParams{OwnerID: owner, CommandKey: doubanSyncNamespace + key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ports.ErrNotFound
	}
	if err != nil {
		return nil, "", ports.ErrUnavailable
	}
	var in ports.DoubanSyncInput
	var c ports.DoubanSyncConfig
	inputDecoder := json.NewDecoder(bytes.NewReader(row.Input))
	inputDecoder.DisallowUnknownFields()
	snapshotDecoder := json.NewDecoder(bytes.NewReader(row.Snapshot))
	snapshotDecoder.DisallowUnknownFields()
	if inputDecoder.Decode(&in) != nil || snapshotDecoder.Decode(&c) != nil || !doubanSyncValid(in) ||
		row.OwnerID != owner || row.CommandKey != doubanSyncNamespace+key || row.RequestHash != doubanSyncHash(in) ||
		c.ID == uuid.Nil || c.ID != row.ConfigID || c.OwnerID != owner || c.Version != row.Version || c.Version != in.ExpectedVersion+1 ||
		c.DoubanUID != in.DoubanUID || c.Enabled != in.Enabled || !doubanSyncSameID(c.MovieRootID, in.MovieRootID) || !doubanSyncSameID(c.TVRootID, in.TVRootID) ||
		c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) {
		return nil, "", ports.ErrSubscriptionBindingsConflict
	}
	current, err := q.GetDoubanSyncConfig(ctx, owner)
	if err != nil {
		return nil, "", ports.ErrUnavailable
	}
	if current.ID != c.ID || current.OwnerID != owner || current.Version < c.Version || !current.CreatedAt.Time.Equal(c.CreatedAt) || current.UpdatedAt.Time.Before(c.UpdatedAt) {
		return nil, "", ports.ErrSubscriptionBindingsConflict
	}
	if current.Version == c.Version {
		currentConfig := doubanSyncConfig(current)
		if !currentConfig.CreatedAt.Equal(c.CreatedAt) || !currentConfig.UpdatedAt.Equal(c.UpdatedAt) {
			return nil, "", ports.ErrSubscriptionBindingsConflict
		}
		// Equal instants may have different offsets in the immutable JSON snapshot.
		currentConfig.CreatedAt, currentConfig.UpdatedAt = c.CreatedAt, c.UpdatedAt
		original, _ := json.Marshal(c)
		actual, _ := json.Marshal(currentConfig)
		if !bytes.Equal(original, actual) {
			return nil, "", ports.ErrSubscriptionBindingsConflict
		}
	}
	return &ports.DoubanSyncConfigReceipt{CommandKey: key, Config: c}, row.RequestHash, nil
}
func (s *DoubanSyncConfigStore) ReadDoubanSyncConfigReceipt(ctx context.Context, actor *ports.Actor, key string) (*ports.DoubanSyncConfigReceipt, error) {
	a, err := doubanSyncCopyActor(actor)
	if err != nil {
		return nil, err
	}
	if !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q, err := s.lockOwner(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	r, _, err := doubanSyncReceipt(ctx, q, uuid.MustParse(a.ID), key)
	return r, err
}
func (s *DoubanSyncConfigStore) SaveDoubanSyncConfig(ctx context.Context, actor *ports.Actor, in ports.DoubanSyncInput, key string) (*ports.DoubanSyncConfigReceipt, error) {
	// Copy caller-owned pointers before any blocking database operation.
	in.MovieRootID = doubanSyncCopyID(in.MovieRootID)
	in.TVRootID = doubanSyncCopyID(in.TVRootID)
	a, err := doubanSyncCopyActor(actor)
	if err != nil {
		return nil, err
	}
	if !doubanSyncValid(in) || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	isolated, err := q.GuardDoubanSyncIsolation(ctx)
	if err != nil || isolated {
		return nil, ports.ErrUnavailable
	}
	q, err = s.lockOwner(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	owner := uuid.MustParse(a.ID)
	old, hash, err := doubanSyncReceipt(ctx, q, owner, key)
	if err == nil {
		if hash != doubanSyncHash(in) {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		return old, nil
	}
	if !errors.Is(err, ports.ErrNotFound) {
		return nil, err
	}
	current, err := q.GetDoubanSyncConfig(ctx, owner)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrUnavailable
	}
	if exists && current.Version != in.ExpectedVersion || !exists && in.ExpectedVersion != 0 {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	roots := []uuid.UUID{}
	if in.MovieRootID != nil {
		roots = append(roots, *in.MovieRootID)
	}
	if in.TVRootID != nil {
		roots = append(roots, *in.TVRootID)
	}
	locked, err := q.LockDoubanSyncRoots(ctx, roots)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	for _, binding := range []struct {
		id   *uuid.UUID
		kind string
	}{{in.MovieRootID, "movie"}, {in.TVRootID, "tv"}} {
		if binding.id == nil {
			continue
		}
		found := false
		for _, root := range locked {
			if root.ID == *binding.id {
				if root.MediaType != binding.kind || in.Enabled && root.Health != "online" {
					return nil, ports.ErrRejected
				}
				found = true
				break
			}
		}
		if !found {
			return nil, ports.ErrNotFound
		}
	}
	var row db.DoubanSyncConfig
	if exists {
		row, err = q.UpdateDoubanSyncConfig(ctx, db.UpdateDoubanSyncConfigParams{OwnerID: owner, DoubanUid: in.DoubanUID, Enabled: in.Enabled, MovieRootID: doubanSyncNullable(in.MovieRootID), TvRootID: doubanSyncNullable(in.TVRootID)})
	} else {
		row, err = q.CreateDoubanSyncConfig(ctx, db.CreateDoubanSyncConfigParams{ID: s.ids.NewUUID(), OwnerID: owner, DoubanUid: in.DoubanUID, Enabled: in.Enabled, MovieRootID: doubanSyncNullable(in.MovieRootID), TvRootID: doubanSyncNullable(in.TVRootID)})
	}
	if err != nil {
		return nil, doubanSyncError(err)
	}
	c := doubanSyncConfig(row)
	raw, _ := json.Marshal(c)
	input, _ := json.Marshal(in)
	err = q.CreateDoubanSyncReceipt(ctx, db.CreateDoubanSyncReceiptParams{OwnerID: owner, CommandKey: doubanSyncNamespace + key, ConfigID: c.ID, Version: c.Version, RequestHash: doubanSyncHash(in), Input: input, Snapshot: raw})
	if err != nil {
		return nil, doubanSyncError(err)
	}
	// Configuration events never claim that automatic synchronization completed.
	payload, _ := json.Marshal(map[string]any{"config_id": c.ID, "owner_id": owner, "version": c.Version, "enabled": c.Enabled})
	if err = q.AuditDoubanSyncConfig(ctx, db.AuditDoubanSyncConfigParams{ID: s.ids.NewUUID(), ActorID: owner.String(), TargetID: c.ID.String(), Payload: payload}); err != nil {
		return nil, doubanSyncError(err)
	}
	if err = q.PublishDoubanSyncConfig(ctx, db.PublishDoubanSyncConfigParams{ID: s.ids.NewUUID(), AggregateID: c.ID.String(), Payload: payload, Seq: c.Version}); err != nil {
		return nil, doubanSyncError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, doubanSyncError(err)
	}
	return &ports.DoubanSyncConfigReceipt{CommandKey: key, Config: c}, nil
}
