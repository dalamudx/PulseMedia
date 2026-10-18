package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type TokenManagementStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewTokenManagementStore(p *pgxpool.Pool, ids ports.IDGen) *TokenManagementStore {
	return &TokenManagementStore{p, ids}
}
func tokenOwner(a *ports.Actor) (uuid.UUID, error) {
	if a == nil {
		return uuid.Nil, ports.ErrUnauthorized
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) {
		return uuid.Nil, ports.ErrForbidden
	}
	return id, nil
}
func tokenStoreError(err *error) {
	if *err != nil && !errors.Is(*err, ports.ErrForbidden) && !errors.Is(*err, ports.ErrUnauthorized) && !errors.Is(*err, ports.ErrNotFound) && !errors.Is(*err, ports.ErrInvalidTokenCommand) && !errors.Is(*err, ports.ErrTokenCommandConflict) {
		*err = ports.ErrUnavailable
	}
}
func tokenUser(ctx context.Context, q *db.Queries, id uuid.UUID) error {
	u, e := q.LockTokenManagementUser(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !u.IsActive || (u.Role != ports.RoleAdmin && u.Role != ports.RoleMember) {
		return ports.ErrForbidden
	}
	return nil
}
func tokenMetadata(id uuid.UUID, name string, scopes []byte, created, expires, revoked pgtype.Timestamptz) (ports.ManagedToken, error) {
	m := ports.ManagedToken{ID: id, Name: name, CreatedAt: created.Time, Scopes: []string{}}
	if e := json.Unmarshal(scopes, &m.Scopes); e != nil || m.Scopes == nil {
		return m, ports.ErrUnavailable
	}
	if expires.Valid {
		v := expires.Time
		m.ExpiresAt = &v
	}
	if revoked.Valid {
		v := revoked.Time
		m.RevokedAt = &v
	}
	return m, nil
}
func (s *TokenManagementStore) ListOwnTokens(ctx context.Context, a *ports.Actor) (out []ports.ManagedToken, err error) {
	id, e := tokenOwner(a)
	if e != nil {
		return nil, e
	}
	defer tokenStoreError(&err)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = tokenUser(ctx, q, id); e != nil {
		return nil, e
	}
	rows, e := q.ListOwnAPITokens(ctx, id)
	if e != nil {
		return nil, e
	}
	out = []ports.ManagedToken{}
	for _, r := range rows {
		m, e := tokenMetadata(r.ID, r.Name, r.Scopes, r.CreatedAt, r.ExpiresAt, r.RevokedAt)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, tx.Commit(ctx)
}
func (s *TokenManagementStore) CreateOwnToken(ctx context.Context, a *ports.Actor, c ports.TokenCommand, hash []byte, now time.Time, key, fp string) (out *ports.TokenCommandResult, err error) {
	return s.command(ctx, a, uuid.Nil, &c, hash, now, key, fp)
}
func (s *TokenManagementStore) RevokeOwnToken(ctx context.Context, a *ports.Actor, id uuid.UUID, key, fp string) (out *ports.TokenCommandResult, err error) {
	return s.command(ctx, a, id, nil, nil, time.Time{}, key, fp)
}
func (s *TokenManagementStore) command(ctx context.Context, a *ports.Actor, target uuid.UUID, c *ports.TokenCommand, hash []byte, now time.Time, key, fp string) (out *ports.TokenCommandResult, err error) {
	owner, e := tokenOwner(a)
	if e != nil {
		return nil, e
	}
	if key == "" || len(key) > 160 || len(fp) != 64 {
		return nil, ports.ErrInvalidTokenCommand
	}
	defer tokenStoreError(&err)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = tokenUser(ctx, q, owner); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "auth.token.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		if old.RequestHash != fp {
			return nil, ports.ErrTokenCommandConflict
		}
		id, e := uuid.Parse(old.ResourceID.String)
		if e != nil || (c == nil && id != target) {
			return nil, ports.ErrTokenCommandConflict
		}
		r, e := q.GetOwnAPIToken(ctx, db.GetOwnAPITokenParams{ID: id, UserID: owner})
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		m, e := tokenMetadata(r.ID, r.Name, r.Scopes, r.CreatedAt, r.ExpiresAt, r.RevokedAt)
		if e != nil {
			return nil, e
		}
		return &ports.TokenCommandResult{Item: m, Replayed: true}, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	var m ports.ManagedToken
	action := "auth.token.revoked"
	if c != nil {
		if len(hash) != 32 || c.Name == "" || len(c.Name) > 120 {
			return nil, ports.ErrInvalidTokenCommand
		}
		expires := pgtype.Timestamptz{}
		if c.ExpiresAt != nil {
			valid, expiryErr := q.IsTokenExpiryFuture(ctx, pgtype.Timestamptz{Time: *c.ExpiresAt, Valid: true})
			if expiryErr != nil {
				return nil, expiryErr
			}
			if !valid || !c.ExpiresAt.After(now) {
				return nil, ports.ErrInvalidTokenCommand
			}
			expires = pgtype.Timestamptz{Time: *c.ExpiresAt, Valid: true}
		}
		r, e := q.CreateAPIToken(ctx, db.CreateAPITokenParams{ID: s.ids.NewUUID(), UserID: owner, Name: c.Name, TokenHash: hash, Scopes: []byte(`["read"]`), ExpiresAt: expires})
		if e != nil {
			return nil, e
		}
		m, e = tokenMetadata(r.ID, r.Name, r.Scopes, r.CreatedAt, r.ExpiresAt, r.RevokedAt)
		if e != nil {
			return nil, e
		}
		action = "auth.token.created"
	} else {
		r, e := q.RevokeOwnAPIToken(ctx, db.RevokeOwnAPITokenParams{ID: target, UserID: owner})
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		m, e = tokenMetadata(r.ID, r.Name, r.Scopes, r.CreatedAt, r.ExpiresAt, r.RevokedAt)
		if e != nil {
			return nil, e
		}
	}
	out = &ports.TokenCommandResult{Item: m}
	raw, e := json.Marshal(out)
	if e != nil {
		return nil, e
	}
	audit, _ := json.Marshal(map[string]any{"id": m.ID, "scopes": m.Scopes, "expires_at": m.ExpiresAt, "revoked_at": m.RevokedAt})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: action, TargetType: "api_token", TargetID: m.ID.String(), Payload: audit}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "api_token", AggregateID: m.ID.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "api_token", AggregateID: m.ID.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "api_token", AggregateID: m.ID.String(), EventType: action, Seq: int64(seq), Payload: audit}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: fp, ResponseCode: 200, ResponseBody: raw, ResourceID: pgtype.Text{String: m.ID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
