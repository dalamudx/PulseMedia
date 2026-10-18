package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
	"time"
)

var ErrInvalidMovieSelection = errors.New("invalid explicit movie selection")
var ErrMovieSelectionConflict = errors.New("movie selection inputs changed")

type MovieSelection struct {
	TMDBID       int64     `json:"tmdb_id"`
	RootID       uuid.UUID `json:"root_id"`
	CandidateID  uuid.UUID `json:"candidate_id"`
	DownloaderID uuid.UUID `json:"downloader_id"`
	PolicyID     string    `json:"quality_policy_id"`
}
type MovieSelectionSnapshot struct {
	Stamp      string
	Rule       media.CandidateRuleInput
	Current    bool
	Reason     string
	ObservedAt time.Time
}
type SelectedMovieMetadata struct {
	TMDBID                       int64
	Title, OriginalTitle, IMDbID string
	Year                         int
}
type MovieSelectionPreview struct {
	Selection    MovieSelection `json:"selection"`
	InputStamp   string         `json:"input_stamp"`
	Title        string         `json:"title"`
	Year         int            `json:"year"`
	Decision     string         `json:"decision"`
	Reason       string         `json:"reason"`
	Score        int            `json:"score"`
	ObservedAt   time.Time      `json:"observed_at"`
	MatchedTitle string         `json:"matched_title"`
}
type MovieSelectionCommand struct {
	Selection          MovieSelection `json:"selection"`
	ExpectedInputStamp string         `json:"expected_input_stamp"`
	Confirmed          bool           `json:"confirmed"`
}
type MovieSelectionReceipt struct {
	OperationID uuid.UUID `json:"operation_id"`
	MediaID     uuid.UUID `json:"media_id"`
	UnitID      uuid.UUID `json:"unit_id"`
	RootID      uuid.UUID `json:"root_id"`
	CandidateID uuid.UUID `json:"candidate_id"`
	Status      string    `json:"status"`
}
type MovieSelectionRepository interface {
	ReadMovieSelection(context.Context, *Actor, MovieSelection) (MovieSelectionSnapshot, error)
	ReadMovieSelectionReceipt(context.Context, *Actor, MovieSelection, string, string) (*MovieSelectionReceipt, error)
	CommitMovieSelection(context.Context, *Actor, MovieSelection, SelectedMovieMetadata, string, string, string) (*MovieSelectionReceipt, error)
}
