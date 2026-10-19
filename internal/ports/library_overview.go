package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Registered imported facts, not current policy satisfaction or physical access.
// Distinct works/units/files are global; root counts must never be summed.
type LibraryOverviewCounts struct {
	Movies   string `json:"movies"`
	TVShows  string `json:"tv_shows"`
	Episodes string `json:"episodes"`
	Files    string `json:"files"`
}

type LibraryRootOverview struct {
	RootID         uuid.UUID  `json:"root_id"`
	Name           string     `json:"name"`
	MediaType      string     `json:"media_type"`
	Health         string     `json:"health"`
	Availability   string     `json:"availability"`
	CapacityState  string     `json:"capacity_state"`
	TotalBytes     *string    `json:"total_bytes,omitempty"`
	AvailableBytes *string    `json:"available_bytes,omitempty"`
	SampledAt      *time.Time `json:"sampled_at,omitempty"`
}

type LibraryRecentImport struct {
	MediaID    uuid.UUID `json:"media_id"`
	MediaType  string    `json:"media_type"`
	Title      string    `json:"title"`
	Year       *int32    `json:"year,omitempty"`
	ImportedAt time.Time `json:"imported_at"`
	PosterURL  *string   `json:"poster_url,omitempty"`
}

type LibraryOverview struct {
	Counts     LibraryOverviewCounts `json:"counts"`
	Roots      []LibraryRootOverview `json:"roots"`
	Recent     []LibraryRecentImport `json:"recent"`
	ObservedAt time.Time             `json:"observed_at"`
}

type LibraryOverviewRequest struct{ RecentLimit int }

// ReadLibraryOverview revalidates the current actor in a read-only consistent
// database snapshot. No filesystem, downloader or metadata-provider I/O.
type LibraryOverviewReader interface {
	ReadLibraryOverview(context.Context, *Actor, LibraryOverviewRequest) (*LibraryOverview, error)
}
