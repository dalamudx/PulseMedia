package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewActivityOperationQueryStore requires schema 93 and enriches authorized operations.
func NewActivityOperationQueryStore(pool *pgxpool.Pool) *OperationQueryStore {
	return &OperationQueryStore{pool: pool, activity: &activityOperationQueryStore{pool: pool}}
}

type activityOperationQueryStore struct{ pool *pgxpool.Pool }

var _ ports.OperationQuery = (*activityOperationQueryStore)(nil)

func (s *activityOperationQueryStore) ReadOperation(ctx context.Context, actor *ports.Actor, id uuid.UUID) (*ports.OperationSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrInvalidOperationQuery
	}
	page, err := s.read(ctx, actor, id, ports.OperationListRequest{View: "all", Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(page.Items) != 1 {
		return nil, ports.ErrUnavailable
	}
	return &page.Items[0].OperationSnapshot, nil
}

func (s *activityOperationQueryStore) ListOperations(ctx context.Context, actor *ports.Actor, request ports.OperationListRequest) (*ports.OperationPage, error) {
	return s.read(ctx, actor, uuid.Nil, request)
}

func (s *activityOperationQueryStore) read(ctx context.Context, actor *ports.Actor, id uuid.UUID, request ports.OperationListRequest) (*ports.OperationPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if actor == nil || actor.Type != ports.ActorTypeUser ||
		(actor.Role != ports.RoleMember && actor.Role != ports.RoleAdmin) ||
		(actor.Scopes != nil && !slices.Contains(actor.Scopes, "read")) {
		return nil, ports.ErrForbidden
	}
	actorID, err := uuid.Parse(actor.ID)
	if err != nil || actorID == uuid.Nil {
		return nil, ports.ErrForbidden
	}
	if request.Limit < 1 || request.Limit > 50 || !slices.Contains([]string{"active", "review", "all"}, request.View) || len(request.Cursor) > 512 {
		return nil, ports.ErrInvalidOperationQuery
	}
	var cursor operationCursor
	if request.Cursor != "" {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(request.Cursor)
		if err != nil || base64.RawURLEncoding.EncodeToString(raw) != request.Cursor || json.Unmarshal(raw, &cursor) != nil {
			return nil, ports.ErrInvalidOperationQuery
		}
		canonical, err := json.Marshal(cursor)
		if err != nil || !bytes.Equal(raw, canonical) || cursor.Actor != actor.ID || cursor.Role != actor.Role || cursor.View != request.View || cursor.ID == uuid.Nil || cursor.At.IsZero() || cursor.At.Year() < 1 || cursor.At.Year() > 9999 {
			return nil, ports.ErrInvalidOperationQuery
		}
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, operationTimelineReadError(ctx)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	user, err := q.GetUserByID(ctx, actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, operationTimelineReadError(ctx)
	}
	if !user.IsActive || user.Role != actor.Role {
		return nil, ports.ErrForbidden
	}
	if id != uuid.Nil {
		visibility, err := q.OperationTimelineVisibility(ctx, db.OperationTimelineVisibilityParams{OperationID: id, ActorID: actor.ID, IsAdmin: actor.IsAdmin()})
		if err != nil {
			return nil, operationTimelineReadError(ctx)
		}
		if !visibility.OperationExists {
			return nil, ports.ErrNotFound
		}
		if !visibility.Visible {
			return nil, ports.ErrForbidden
		}
	}
	limit := request.Limit + 1
	if id != uuid.Nil {
		limit = 1
	}
	rows, err := q.ListActivityOperationSummaries(ctx, db.ListActivityOperationSummariesParams{
		OperationFilter: id, IsAdmin: actor.IsAdmin(), ActorID: actor.ID, View: request.View,
		HasCursor: request.Cursor != "", CursorTime: pgtype.Timestamptz{Time: cursor.At, Valid: true}, CursorID: cursor.ID, PageLimit: int32(limit),
	})
	if err != nil {
		return nil, operationTimelineReadError(ctx)
	}
	more := len(rows) > request.Limit
	if more {
		rows = rows[:request.Limit]
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.OperationID
	}
	telemetry, err := ReadOperationTelemetry(ctx, q, ids)
	if err != nil {
		return nil, operationTimelineReadError(ctx)
	}
	out := &ports.OperationPage{Items: make([]ports.OperationListEntry, 0, len(rows))}
	for _, row := range rows {
		item, ok := telemetry[row.OperationID]
		if !ok {
			return nil, ports.ErrUnavailable
		}
		entry := ports.OperationListEntry{OperationSnapshot: ports.OperationSnapshot{OperationID: row.OperationID, Status: row.Status, Telemetry: &item}, Title: row.Title, CreatedAt: row.CreatedAt.Time}
		if row.DownloadStatus != "" {
			entry.DownloadStatus = &row.DownloadStatus
		}
		if row.PlanStatus != "" {
			entry.PlanStatus = &row.PlanStatus
		}
		if row.PlanID != uuid.Nil {
			entry.PlanID = &row.PlanID
		}
		if item.Progress != nil && !math.IsNaN(*item.Progress) && !math.IsInf(*item.Progress, 0) && *item.Progress >= 0 && *item.Progress <= 1 {
			progress := float32(*item.Progress)
			if *item.Progress < 1 && progress == 1 {
				progress = math.Nextafter32(1, 0)
			}
			entry.Progress = &progress
		}
		out.Items = append(out.Items, entry)
	}
	if more {
		last := out.Items[len(out.Items)-1]
		raw, err := json.Marshal(operationCursor{Actor: actor.ID, Role: actor.Role, View: request.View, At: last.CreatedAt, ID: last.OperationID})
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, operationTimelineReadError(ctx)
	}
	return out, nil
}
