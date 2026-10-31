package postgres

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type fileMappingCursor struct {
	RootID    uuid.UUID `json:"root_id"`
	FileID    uuid.UUID `json:"file_id"`
	CreatedAt time.Time `json:"created_at"`
}

func encodeFileMappingCursor(root, file uuid.UUID, created time.Time) string {
	raw, _ := json.Marshal(fileMappingCursor{RootID: root, FileID: file, CreatedAt: created.UTC()})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeFileMappingCursor(query ports.FileMappingOptionsQuery) (time.Time, uuid.UUID, error) {
	if query.Cursor == "" {
		return time.Time{}, uuid.Nil, nil
	}
	if len(query.Cursor) > 512 {
		return time.Time{}, uuid.Nil, ports.ErrInvalidFileMapping
	}
	raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	var cursor fileMappingCursor
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.RootID != query.RootID || cursor.FileID == uuid.Nil || cursor.CreatedAt.IsZero() || cursor.CreatedAt.Year() < 1 || cursor.CreatedAt.Year() > 9999 || encodeFileMappingCursor(cursor.RootID, cursor.FileID, cursor.CreatedAt) != query.Cursor {
		return time.Time{}, uuid.Nil, ports.ErrInvalidFileMapping
	}
	return cursor.CreatedAt, cursor.FileID, nil
}
