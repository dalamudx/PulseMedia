package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/command"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
)

func (s *SettingsStore) ReadSettingReceipt(ctx context.Context, a *ports.Actor, key, requestKey string) (out *ports.SettingCommandResult, err error) {
	if e := command.AuthorizeSession(a, ports.RoleAdmin); e != nil {
		return nil, e
	}
	defer settingStoreError(&err)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = settingUser(ctx, q, a, true); e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: requestKey})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if old.RequestHash == "" || old.ResponseCode != 200 || !old.ResourceID.Valid || old.ResourceID.String != key {
		return nil, ports.ErrCommandConflict
	}
	var item ports.SettingItem
	if e = json.Unmarshal(old.ResponseBody, &item); e != nil || item.Key != key {
		return nil, ports.ErrCommandConflict
	}
	if key == "tmdb.api_key" {
		item.Value = map[string]any{"configured": true}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &ports.SettingCommandResult{Data: &item, StatusCode: 200, ResourceID: key, FromCache: true}, nil
}
