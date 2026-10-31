package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type mappingMediaCursor struct {
	ID        uuid.UUID `json:"id"`
	UpdatedAt time.Time `json:"updated_at"`
	Filter    string    `json:"filter"`
}

func mappingMediaFilter(q ports.FileMappingOptionsQuery) string {
	raw, _ := json.Marshal([]any{q.MediaQuery, q.MediaType, q.TMDBID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func encodeMappingMediaCursor(id uuid.UUID, at time.Time, filter string) string {
	raw, _ := json.Marshal(mappingMediaCursor{ID: id, UpdatedAt: at.UTC(), Filter: filter})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeMappingMediaCursor(q ports.FileMappingOptionsQuery) (time.Time, uuid.UUID, error) {
	if q.MediaCursor == "" {
		return time.Time{}, uuid.Nil, nil
	}
	if !q.Valid() {
		return time.Time{}, uuid.Nil, ports.ErrInvalidFileMapping
	}
	raw, e := base64.RawURLEncoding.DecodeString(q.MediaCursor)
	var c mappingMediaCursor
	if e != nil || json.Unmarshal(raw, &c) != nil || c.ID == uuid.Nil || c.UpdatedAt.IsZero() || c.UpdatedAt.Year() < 1 || c.UpdatedAt.Year() > 9999 || c.Filter != mappingMediaFilter(q) || encodeMappingMediaCursor(c.ID, c.UpdatedAt, c.Filter) != q.MediaCursor {
		return time.Time{}, uuid.Nil, ports.ErrInvalidFileMapping
	}
	return c.UpdatedAt, c.ID, nil
}
func readMappingMediaOptions(ctx context.Context, q *db.Queries, query ports.FileMappingOptionsQuery, out *ports.FileMappingOptions) error {
	if !query.Valid() {
		return ports.ErrInvalidFileMapping
	}
	before, id, e := decodeMappingMediaCursor(query)
	if e != nil {
		return e
	}
	rows, e := q.SearchFileMappingMediaOptions(ctx, db.SearchFileMappingMediaOptionsParams{TitleQuery: query.MediaQuery, MediaType: query.MediaType, TmdbID: query.TMDBID, BeforeUpdatedAt: pgtype.Timestamptz{Time: before, Valid: !before.IsZero()}, BeforeID: id})
	if e != nil {
		return ports.ErrUnavailable
	}
	if len(rows) > out.Limit {
		out.MediaHasMore = true
		rows = rows[:out.Limit]
		last := rows[len(rows)-1]
		out.MediaNextCursor = encodeMappingMediaCursor(last.ID, last.UpdatedAt.Time, mappingMediaFilter(query))
	}
	for _, m := range rows {
		out.Media = append(out.Media, ports.FileMappingMediaOption{ID: m.ID, Title: m.Title, MediaType: m.MediaType})
	}
	// A saved selection is read by identity, independently of the current page/filter.
	if query.MediaID != uuid.Nil {
		m, err := q.GetMediaByID(ctx, query.MediaID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return ports.ErrUnavailable
		}
		if err == nil {
			out.SelectedMedia = &ports.FileMappingMediaOption{ID: m.ID, Title: m.Title, MediaType: m.MediaType}
		}
	}
	return nil
}
