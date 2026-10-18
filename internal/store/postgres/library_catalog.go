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
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
)

type LibraryCatalogStore struct{ pool *pgxpool.Pool }

func NewLibraryCatalogStore(p *pgxpool.Pool) *LibraryCatalogStore {
	return &LibraryCatalogStore{pool: p}
}

type libraryCursor struct {
	Actor, Role string
	File, Unit  uuid.UUID
}

func (s *LibraryCatalogStore) ListLibraryCatalog(ctx context.Context, a *ports.Actor, r ports.LibraryCatalogRequest) (*ports.LibraryCatalogPage, error) {
	if a == nil || a.Type != ports.ActorTypeUser {
		return nil, ports.ErrForbidden
	}
	if r.Limit == 0 {
		r.Limit = 50
	}
	if r.Limit < 1 || r.Limit > 100 || len(r.Cursor) > 1024 {
		return nil, ports.ErrInvalidLibraryQuery
	}
	id, e := uuid.Parse(a.ID)
	if e != nil {
		return nil, ports.ErrForbidden
	}
	cursor := libraryCursor{Actor: a.ID, Role: string(a.Role)}
	if r.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(r.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Actor != a.ID || cursor.Role != string(a.Role) || cursor.File == uuid.Nil || cursor.Unit == uuid.Nil {
			return nil, ports.ErrInvalidLibraryQuery
		}
	}
	if s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	user, e := q.GetUserByID(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if !user.IsActive || user.Role != string(a.Role) {
		return nil, ports.ErrForbidden
	}
	rows, e := q.ListLibraryCatalog(ctx, db.ListLibraryCatalogParams{AfterFile: cursor.File, AfterUnit: cursor.Unit, RowLimit: int32(r.Limit + 1)})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	page := &ports.LibraryCatalogPage{Items: []ports.LibraryCatalogEntry{}}
	more := len(rows) > r.Limit
	if more {
		rows = rows[:r.Limit]
	}
	for _, v := range rows {
		var year *int32
		if v.Year.Valid {
			value := v.Year.Int32
			year = &value
		}
		page.Items = append(page.Items, ports.LibraryCatalogEntry{FileID: v.FileID, UnitID: v.UnitID, MediaID: v.MediaID, RootID: v.RootID, Title: v.Title, Year: year, MediaType: v.MediaType, UnitKey: v.UnitKey, RootName: v.RootName, RootHealth: v.RootHealth, RelativePath: v.RelPath, SizeBytes: strconv.FormatInt(v.SizeBytes, 10), Imported: v.Imported, Confirmed: v.Confirmed})
	}
	if more {
		last := rows[len(rows)-1]
		raw, err := json.Marshal(libraryCursor{Actor: a.ID, Role: string(a.Role), File: last.FileID, Unit: last.UnitID})
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return page, nil
}
