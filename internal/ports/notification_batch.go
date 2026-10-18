package ports

import "github.com/google/uuid"

// Immutable source references only: never file paths, credentials or arbitrary titles.
type NotificationBatchMember struct {
	SourceEventID uuid.UUID `json:"source_event_id"`
	UnitID        uuid.UUID `json:"unit_id"`
	RootID        uuid.UUID `json:"root_id"`
	FileID        uuid.UUID `json:"file_id"`
}
type NotificationBatchPayload struct {
	Version string                    `json:"version"`
	MediaID uuid.UUID                 `json:"media_id"`
	Members []NotificationBatchMember `json:"members"`
}
