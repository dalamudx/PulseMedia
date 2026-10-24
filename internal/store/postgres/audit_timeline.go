package postgres

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/auditlog"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditTimelineStore struct{ pool *pgxpool.Pool }

func NewAuditTimelineStore(pool *pgxpool.Pool) *AuditTimelineStore {
	return &AuditTimelineStore{pool: pool}
}

var _ ports.AuditTimelineReader = (*AuditTimelineStore)(nil)

func (s *AuditTimelineStore) ReadAuditTimeline(ctx context.Context, actor *ports.Actor, filter auditlog.Filter, before *auditlog.Position, limit int) (out []ports.AuditSummary, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrRejected) {
			err = ports.ErrUnavailable
		}
		if err != nil {
			out = nil
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil && (len(actor.Scopes) != 1 || actor.Scopes[0] != "read") {
		return nil, ports.ErrForbidden
	}
	account, err := uuid.Parse(actor.ID)
	if err != nil || account == uuid.Nil {
		return nil, ports.ErrForbidden
	}
	filter, err = auditlog.Normalize(filter)
	if err != nil || limit < 1 || limit > auditlog.MaximumLimit+1 || before != nil && !before.Valid() {
		return nil, ports.ErrRejected
	}
	params := db.ReadAuditTimelineParams{
		ActorType: filter.ActorType, ActorID: filter.ActorID, Action: filter.Action, TargetType: filter.TargetType, TargetID: filter.TargetID,
		FromTime: pgtype.Timestamptz{Time: filter.From, Valid: !filter.From.IsZero()}, UntilTime: pgtype.Timestamptz{Time: filter.Until, Valid: !filter.Until.IsZero()}, PageLimit: int32(limit),
	}
	if before != nil {
		params.BeforeTime = pgtype.Timestamptz{Time: before.CreatedAt.UTC(), Valid: true}
		params.BeforeID = pgtype.UUID{Bytes: before.ID, Valid: true}
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	q := db.New(tx)
	user, err := q.LockSubscriptionListAccount(ctx, account)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if !user.IsActive || user.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if err = q.SetAuditTimelineReadOnly(ctx); err != nil {
		return nil, err
	}
	rows, err := q.ReadAuditTimeline(ctx, params)
	if err != nil {
		return nil, err
	}
	out = make([]ports.AuditSummary, 0, len(rows))
	for _, row := range rows {
		if !row.CreatedAt.Valid || row.CreatedAt.InfinityModifier != pgtype.Finite || !(auditlog.Position{ID: row.ID, CreatedAt: row.CreatedAt.Time}).Valid() {
			return nil, ports.ErrUnavailable
		}
		out = append(out, ports.AuditSummary{ID: row.ID, ActorType: row.ActorType, ActorID: row.ActorID, Action: row.Action, TargetType: row.TargetType, TargetID: row.TargetID, CreatedAt: row.CreatedAt.Time.UTC()})
	}
	return out, nil
}
