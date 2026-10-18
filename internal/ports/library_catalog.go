package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrInvalidLibraryQuery = errors.New("invalid library query")

type LibraryCatalogRequest struct {
	Cursor string
	Limit  int
}
type LibraryCatalogEntry struct {
	FileID       uuid.UUID `json:"file_id"`
	UnitID       uuid.UUID `json:"unit_id"`
	MediaID      uuid.UUID `json:"media_id"`
	RootID       uuid.UUID `json:"root_id"`
	Title        string    `json:"title"`
	Year         *int32    `json:"year,omitempty"`
	MediaType    string    `json:"media_type"`
	UnitKey      string    `json:"unit_key"`
	RootName     string    `json:"root_name"`
	RootHealth   string    `json:"root_health"`
	RelativePath string    `json:"relative_path"`
	SizeBytes    string    `json:"size_bytes"`
	Imported     bool      `json:"imported"`
	Confirmed    bool      `json:"confirmed"`
}
type LibraryCatalogPage struct {
	Items      []LibraryCatalogEntry `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}
type LibraryCatalogReader interface {
	ListLibraryCatalog(context.Context, *Actor, LibraryCatalogRequest) (*LibraryCatalogPage, error)
}
