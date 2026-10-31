package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrInvalidFileMapping = errors.New("invalid file mapping command")
var ErrFileMappingConflict = errors.New("file mapping version or evidence changed")

type FileMappingUnit struct {
	ID              uuid.UUID `json:"id"`
	MediaID         uuid.UUID `json:"media_id"`
	UnitKey         string    `json:"unit_key"`
	MetadataVersion int64     `json:"metadata_version"`
	Retired         bool      `json:"retired"`
}
type FileMappingAssociation struct {
	Unit   FileMappingUnit `json:"unit"`
	Active bool            `json:"active"`
}
type FileMappingPreview struct {
	FileID          uuid.UUID                `json:"file_id"`
	RootID          uuid.UUID                `json:"root_id"`
	RelPath         string                   `json:"rel_path"`
	MediaID         uuid.UUID                `json:"media_id"`
	MediaTitle      string                   `json:"media_title"`
	MappingRevision int64                    `json:"mapping_revision"`
	InputStamp      string                   `json:"input_stamp"`
	CanCorrect      bool                     `json:"can_correct"`
	HoldReason      string                   `json:"hold_reason"`
	AvailableUnits  []FileMappingUnit        `json:"available_units"`
	Associations    []FileMappingAssociation `json:"associations"`
}
type FileMappingCommand struct {
	MediaID           uuid.UUID   `json:"media_id"`
	UnitIDs           []uuid.UUID `json:"unit_ids"`
	ExpectedStamp     string      `json:"expected_stamp"`
	ConfirmedCoverage bool        `json:"confirmed_coverage"`
}
type FileMappingReceipt struct {
	FileID          uuid.UUID   `json:"file_id"`
	MediaID         uuid.UUID   `json:"media_id"`
	MappingRevision int64       `json:"mapping_revision"`
	UnitIDs         []uuid.UUID `json:"unit_ids"`
}
type FileMappingOptionsQuery struct {
	RootID      uuid.UUID
	Cursor      string
	MediaQuery  string
	MediaType   string
	TMDBID      int64
	MediaCursor string
	MediaID     uuid.UUID
}

type FileMappingOptions struct {
	Files []FileMappingOption      `json:"files"`
	Media []FileMappingMediaOption `json:"media"`
	Limit int                      `json:"limit"`

	FilesNextCursor string                  `json:"files_next_cursor"`
	FilesHasMore    bool                    `json:"files_has_more"`
	MediaNextCursor string                  `json:"media_next_cursor"`
	MediaHasMore    bool                    `json:"media_has_more"`
	SelectedMedia   *FileMappingMediaOption `json:"selected_media,omitempty"`
}
type FileMappingOption struct {
	ID       uuid.UUID `json:"id"`
	RootID   uuid.UUID `json:"root_id"`
	RelPath  string    `json:"rel_path"`
	RootName string    `json:"root_name"`
}
type FileMappingMediaOption struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	MediaType string    `json:"media_type"`
}
type FileMappingRepository interface {
	ReadFileMappingOptions(context.Context, *Actor, FileMappingOptionsQuery) (*FileMappingOptions, error)
	ReadFileMappingPreview(context.Context, *Actor, uuid.UUID, uuid.UUID) (*FileMappingPreview, error)
	CorrectFileMapping(context.Context, *Actor, uuid.UUID, FileMappingCommand, string, string) (*FileMappingReceipt, error)
}
