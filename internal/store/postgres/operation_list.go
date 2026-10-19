package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"time"
)

type operationCursor struct {
	Actor string    `json:"a"`
	Role  string    `json:"r"`
	View  string    `json:"v"`
	At    time.Time `json:"t"`
	ID    uuid.UUID `json:"i"`
}

func operationReadPermission(ctx context.Context, q *db.Queries, a *ports.Actor) error {
	if a == nil || a.Type != ports.ActorTypeUser {
		return ports.ErrForbidden
	}
	if len(a.Scopes) > 0 {
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
	uid, e := uuid.Parse(a.ID)
	if e != nil {
		return ports.ErrForbidden
	}
	user, e := q.GetUserByID(ctx, uid)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !user.IsActive || user.Role != a.Role {
		return ports.ErrForbidden
	}
	return nil
}
func (s *OperationQueryStore) ListOperations(ctx context.Context, a *ports.Actor, request ports.OperationListRequest) (*ports.OperationPage, error) {
	if s != nil && s.activity != nil {
		return s.activity.ListOperations(ctx, a, request)
	}
	if a == nil {
		return nil, ports.ErrForbidden
	}
	if request.Limit < 1 || request.Limit > 50 || (request.View != "active" && request.View != "review" && request.View != "all") || len(request.Cursor) > 512 {
		return nil, ports.ErrInvalidOperationQuery
	}
	cursor := operationCursor{Actor: a.ID, Role: a.Role, View: request.View}
	if request.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(request.Cursor)
		if e != nil {
			return nil, ports.ErrInvalidOperationQuery
		}
		if e = json.Unmarshal(raw, &cursor); e != nil || cursor.Actor != a.ID || cursor.Role != a.Role || cursor.View != request.View || cursor.ID == uuid.Nil || cursor.At.IsZero() {
			return nil, ports.ErrInvalidOperationQuery
		}
	}
	if s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if e = operationReadPermission(ctx, q, a); e != nil {
		return nil, e
	}
	rows, e := q.ListVisibleOperationSummaries(ctx, db.ListVisibleOperationSummariesParams{IsAdmin: a.IsAdmin(), ActorID: a.ID, View: request.View, HasCursor: request.Cursor != "", CursorTime: pgtype.Timestamptz{Time: cursor.At, Valid: true}, CursorID: cursor.ID, PageLimit: int32(request.Limit + 1)})
	if e != nil {
		return nil, e
	}
	out := &ports.OperationPage{Items: make([]ports.OperationListEntry, 0, request.Limit)}
	more := len(rows) > request.Limit
	if more {
		rows = rows[:request.Limit]
	}
	for _, r := range rows {
		entry := ports.OperationListEntry{OperationSnapshot: ports.OperationSnapshot{OperationID: r.OperationID, Status: r.Status}, Title: r.Title, CreatedAt: r.CreatedAt.Time}
		if r.DownloadStatus != "" {
			entry.DownloadStatus = &r.DownloadStatus
		}
		if r.Progress.Valid {
			entry.Progress = &r.Progress.Float32
		}
		if r.PlanStatus != "" {
			entry.PlanStatus = &r.PlanStatus
		}
		if r.PlanID != uuid.Nil {
			id := r.PlanID
			entry.PlanID = &id
		}
		out.Items = append(out.Items, entry)
	}
	if more {
		last := out.Items[len(out.Items)-1]
		raw, e := json.Marshal(operationCursor{Actor: a.ID, Role: a.Role, View: request.View, At: last.CreatedAt, ID: last.OperationID})
		if e != nil {
			return nil, e
		}
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
