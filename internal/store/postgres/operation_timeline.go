package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OperationTimelineStore struct{ pool *pgxpool.Pool }

var _ ports.OperationTimelineReader = (*OperationTimelineStore)(nil)

func NewOperationTimelineStore(pool *pgxpool.Pool) *OperationTimelineStore {
	return &OperationTimelineStore{pool: pool}
}

type operationTimelineCursor struct {
	Actor     string    `json:"a"`
	Role      string    `json:"r"`
	Operation uuid.UUID `json:"o"`
	Plan      uuid.UUID `json:"p"`
	Order     int32     `json:"n"`
	Step      uuid.UUID `json:"s"`
}

func (s *OperationTimelineStore) ReadOperationTimeline(ctx context.Context, actor *ports.Actor, operationID uuid.UUID, request ports.OperationTimelineRequest) (*ports.OperationTimelinePage, error) {
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
	if request.Limit == 0 {
		request.Limit = 50
	}
	if operationID == uuid.Nil || request.Limit < 1 || request.Limit > 100 || len(request.Cursor) > 1024 {
		return nil, ports.ErrInvalidOperationQuery
	}
	var cursor operationTimelineCursor
	if request.Cursor != "" {
		raw, decodeErr := base64.RawURLEncoding.Strict().DecodeString(request.Cursor)
		if decodeErr != nil || json.Unmarshal(raw, &cursor) != nil {
			return nil, ports.ErrInvalidOperationQuery
		}
		canonical, _ := json.Marshal(cursor)
		if !bytes.Equal(raw, canonical) || cursor.Actor != actor.ID || cursor.Role != actor.Role || cursor.Operation != operationID || cursor.Plan == uuid.Nil || cursor.Step == uuid.Nil {
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
	visibility, err := q.OperationTimelineVisibility(ctx, db.OperationTimelineVisibilityParams{OperationID: operationID, ActorID: actor.ID, IsAdmin: actor.IsAdmin()})
	if err != nil {
		return nil, operationTimelineReadError(ctx)
	}
	if !visibility.OperationExists {
		return nil, ports.ErrNotFound
	}
	if !visibility.Visible {
		return nil, ports.ErrForbidden
	}
	observed, err := q.OperationTimelineSnapshotTime(ctx)
	if err != nil {
		return nil, operationTimelineReadError(ctx)
	}
	out := &ports.OperationTimelinePage{OperationID: operationID, Items: make([]ports.OperationTimelineEntry, 0), ObservedAt: observed.Time}
	plan, err := q.GetTransferPlanByOpID(ctx, operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		if request.Cursor != "" {
			return nil, ports.ErrInvalidOperationQuery
		}
	} else {
		if err != nil {
			return nil, operationTimelineReadError(ctx)
		}
		if request.Cursor != "" && cursor.Plan != plan.ID {
			return nil, ports.ErrInvalidOperationQuery
		}
		out.PlanID, out.PlanStatus = &plan.ID, &plan.State
		rows, err := q.ListOperationTimelineSteps(ctx, db.ListOperationTimelineStepsParams{PlanID: plan.ID, HasCursor: request.Cursor != "", CursorOrder: cursor.Order, CursorID: cursor.Step, PageLimit: int32(request.Limit + 1)})
		if err != nil {
			return nil, operationTimelineReadError(ctx)
		}
		more := len(rows) > request.Limit
		if more {
			rows = rows[:request.Limit]
		}
		for _, row := range rows {
			relative := row.TargetRel.String
			if !row.TargetRel.Valid || relative == "" {
				relative = row.SourceRel.String
			}
			item := ports.OperationTimelineEntry{StepID: row.ID, Order: row.StepOrder, Action: row.Action, State: row.State, FileName: operationTimelineFileName(relative), UpdatedAt: row.UpdatedAt.Time}
			if row.FinishedAt.Valid {
				at := row.FinishedAt.Time
				item.FinishedAt = &at
			}
			out.Items = append(out.Items, item)
		}
		if more {
			last := rows[len(rows)-1]
			raw, _ := json.Marshal(operationTimelineCursor{Actor: actor.ID, Role: actor.Role, Operation: operationID, Plan: plan.ID, Order: last.StepOrder, Step: last.ID})
			out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, operationTimelineReadError(ctx)
	}
	return out, nil
}

func operationTimelineReadError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ports.ErrUnavailable
}

func operationTimelineFileName(relative string) string {
	// Frozen relative paths are labels only; absolute paths and untrusted syntax stay private.
	if relative == "." || !utf8.ValidString(relative) || !fs.ValidPath(relative) || strings.ContainsAny(relative, "\\:%?#") || strings.ContainsFunc(relative, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return ""
	}
	return path.Base(relative)
}
