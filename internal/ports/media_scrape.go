package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// MediaScrapeCandidate is one active imported file/unit pair that can receive a
// sidecar job. Status is derived from the existing media_sidecar job for its
// imported event and never grants execution authority by itself.
type MediaScrapeCandidate struct {
	SourceEventID uuid.UUID
	FileID        uuid.UUID
	UnitID        uuid.UUID
	MediaID       uuid.UUID
	MediaType     string
	Title         string
	RelPath       string
	Status        MediaScrapeStatus
}

type MediaScrapeStatus string

const (
	MediaScrapeMissing  MediaScrapeStatus = "missing"
	MediaScrapeQueued   MediaScrapeStatus = "queued"
	MediaScrapeRunning  MediaScrapeStatus = "running"
	MediaScrapeComplete MediaScrapeStatus = "complete"
	MediaScrapeReview   MediaScrapeStatus = "review"
)

// MediaScrapePreview is a read-only count of the work a batch would queue.
type MediaScrapePreview struct {
	Total      int
	Missing    int
	Queued     int
	Running    int
	Complete   int
	Review     int
	Candidates []MediaScrapeCandidate
}

// MediaScrapeProgress aggregates the queued jobs of one batch.
type MediaScrapeProgress struct {
	ID         uuid.UUID
	CommandKey string
	CreatedAt  time.Time
	Total      int
	Queued     int
	Running    int
	Succeeded  int
	Review     int
}

// MediaScrapeBatchRepository is the narrow persistence boundary for library
// wide sidecar gap filling.
type MediaScrapeBatchRepository interface {
	ListMediaScrapeCandidates(ctx context.Context, actor *Actor) ([]MediaScrapeCandidate, error)
	StartMediaScrape(ctx context.Context, actor *Actor, commandKey string, allocate func() uuid.UUID) (MediaScrapeProgress, bool, error)
	ReadMediaScrapeBatch(ctx context.Context, actor *Actor, commandKey string) (*MediaScrapeProgress, error)
}
