package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/command"
	"github.com/dalamudx/PulseMedia/internal/domain/artwork"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	tvdbconfig "github.com/dalamudx/PulseMedia/internal/domain/tvdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SettingsStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewSettingsStore(p *pgxpool.Pool, ids ports.IDGen) *SettingsStore { return &SettingsStore{p, ids} }

var _ ports.SettingsRepository = (*SettingsStore)(nil)

func settingStoreError(e *error) {
	if *e != nil && !errors.Is(*e, ports.ErrForbidden) && !errors.Is(*e, ports.ErrUnauthorized) && !errors.Is(*e, ports.ErrCommandConflict) && !errors.Is(*e, ports.ErrSettingVersionConflict) && !errors.Is(*e, ports.ErrInvalidSettingCommand) && !errors.Is(*e, ports.ErrNotFound) {
		*e = ports.ErrUnavailable
	}
}
func settingRecord(row db.Setting) ports.SettingRecord {
	return ports.SettingRecord{Key: row.Key, Value: row.Value, Version: row.Version, UpdatedAt: row.UpdatedAt.Time}
}
func (s *SettingsStore) ReadSetting(ctx context.Context, key string) (out *ports.SettingRecord, err error) {
	defer settingStoreError(&err)
	row, e := db.New(s.pool).GetSetting(ctx, key)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	v := settingRecord(row)
	return &v, nil
}
func settingUser(ctx context.Context, q *db.Queries, a *ports.Actor, requireSession bool) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || (requireSession && a.Scopes != nil) {
		return ports.ErrForbidden
	}
	if a.Scopes != nil {
		read := false
		for _, v := range a.Scopes {
			if v == "read" {
				read = true
			}
		}
		if !read {
			return ports.ErrForbidden
		}
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil {
		return ports.ErrForbidden
	}
	u, e := q.LockSettingsUser(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !u.IsActive || u.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}
func (s *SettingsStore) ListSettings(ctx context.Context, a *ports.Actor) (out []ports.SettingRecord, err error) {
	if a == nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	defer settingStoreError(&err)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = settingUser(ctx, q, a, false); e != nil {
		return nil, e
	}
	rows, e := q.ListSettings(ctx)
	if e != nil {
		return nil, e
	}
	out = make([]ports.SettingRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, settingRecord(row))
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *SettingsStore) UpdateSetting(ctx context.Context, a *ports.Actor, c ports.SettingWrite) (out *ports.SettingCommandResult, err error) {
	if e := command.AuthorizeSession(a, ports.RoleAdmin); e != nil {
		return nil, e
	}
	if c.Key == "" || len(c.Key) > 255 || c.ExpectedVersion < 0 || c.Fingerprint == "" || len(c.IdempotencyKey) > 200 || !json.Valid(c.Value) || !json.Valid(c.Audit) {
		return nil, ports.ErrInvalidSettingCommand
	}
	defer settingStoreError(&err)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	// Shared locks serialize against user revocation/demotion while preserving independent reads.
	if e = settingUser(ctx, q, a, true); e != nil {
		return nil, e
	}
	if c.IdempotencyKey != "" {
		lock, _ := json.Marshal([]string{string(a.Type), a.ID, c.IdempotencyKey})
		if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "command.receipt", AggregateID: string(lock)}); e != nil {
			return nil, e
		}
		old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: c.IdempotencyKey})
		if e == nil {
			if (old.RequestHash != c.Fingerprint && (c.LegacyFingerprint == "" || old.RequestHash != c.LegacyFingerprint)) || !old.ResourceID.Valid || old.ResourceID.String != c.Key || old.ResponseCode != 200 {
				return nil, ports.ErrCommandConflict
			}
			var item ports.SettingItem
			if e = json.Unmarshal(old.ResponseBody, &item); e != nil || item.Key != c.Key {
				return nil, ports.ErrCommandConflict
			}
			if c.Key == "tmdb.api_key" || c.Key == artwork.APIKeySetting || c.Key == tvdbconfig.CredentialsSetting {
				item.Value = map[string]any{"configured": true}
			}
			if e = tx.Commit(ctx); e != nil {
				return nil, e
			}
			return &ports.SettingCommandResult{Data: &item, StatusCode: 200, ResourceID: c.Key, FromCache: true}, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	if c.Key == tvdbconfig.FallbackSetting {
		if _, e := tvdbconfig.DecodeEnabled(c.Value); e != nil {
			return nil, ports.ErrInvalidSettingCommand
		}
	}
	if c.Key == artwork.SwitchSetting {
		if _, e := artwork.DecodeSwitch(c.Value); e != nil {
			return nil, ports.ErrInvalidSettingCommand
		}
	}
	if (c.Key == artwork.APIKeySetting || c.Key == tvdbconfig.CredentialsSetting) && !artwork.ValidCiphertext(c.Value) {
		return nil, ports.ErrInvalidSettingCommand
	}
	if c.Key == naming.TemplateSettingKey {
		if _, e := naming.DecodeTemplateRules(c.Value); e != nil {
			return nil, ports.ErrInvalidSettingCommand
		}
	}
	// Lock the identity even before a row exists; FOR UPDATE alone cannot serialize first creation.
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "setting", AggregateID: c.Key}); e != nil {
		return nil, e
	}
	row, e := q.GetSettingForUpdate(ctx, c.Key)
	if e == nil {
		if row.Version != c.ExpectedVersion {
			return nil, ports.ErrSettingVersionConflict
		}
	} else if errors.Is(e, pgx.ErrNoRows) {
		if c.ExpectedVersion != 0 {
			return nil, ports.ErrSettingVersionConflict
		}
	} else {
		return nil, e
	}
	updated, e := q.SetSetting(ctx, db.SetSettingParams{Key: c.Key, Value: c.Value})
	if e != nil {
		return nil, e
	}
	var value any
	if c.Key == "tmdb.api_key" || c.Key == artwork.APIKeySetting || c.Key == tvdbconfig.CredentialsSetting {
		value = map[string]any{"configured": true}
	} else if e = json.Unmarshal(updated.Value, &value); e != nil {
		return nil, e
	}
	item := ports.SettingItem{Key: updated.Key, Value: value, Version: updated.Version, UpdatedAt: updated.UpdatedAt.Time}
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "setting.update", TargetType: "setting", TargetID: c.Key, Payload: c.Audit}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "setting", AggregateID: c.Key})
	if e != nil {
		return nil, e
	}
	payload, e := json.Marshal(map[string]any{"key": c.Key, "version": updated.Version, "secret": c.Key == "tmdb.api_key" || c.Key == artwork.APIKeySetting || c.Key == tvdbconfig.CredentialsSetting})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "setting", AggregateID: c.Key, EventType: "setting.updated", Seq: int64(seq), Payload: payload}); e != nil {
		return nil, e
	}
	if c.IdempotencyKey != "" {
		body, e := json.Marshal(item)
		if e != nil {
			return nil, e
		}
		if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: c.IdempotencyKey, RequestHash: c.Fingerprint, ResponseCode: 200, ResponseBody: body, ResourceID: pgtype.Text{String: c.Key, Valid: true}}); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &ports.SettingCommandResult{Data: &item, StatusCode: 200, ResourceID: c.Key}, nil
}
