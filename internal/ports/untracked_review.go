package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const UntrackedReviewJobKind = "untracked_review"

var ErrUntrackedReviewStale = errors.New("untracked review material changed")

// A zero TMDBID requests a suggestion; a positive ID is explicit typed intent.
type UntrackedReviewInput struct {
	ScopeVersion         int64  `json:"scope_version"`
	FileRevision         int64  `json:"file_revision"`
	MediaType            string `json:"media_type"`
	TMDBID               int64  `json:"tmdb_id"`
	CanonicalUnitID      string `json:"canonical_unit_id,omitempty"`
	MediaMetadataVersion int64  `json:"media_metadata_version,omitempty"`
	UnitMetadataVersion  int64  `json:"unit_metadata_version,omitempty"`
	Season               int    `json:"season,omitempty"`
	Episode              int    `json:"episode,omitempty"`
	TMDBEpisodeID        int64  `json:"tmdb_episode_id,omitempty"`
}

type UntrackedReviewRequest struct {
	ID        uuid.UUID            `json:"id"`
	ScopeID   uuid.UUID            `json:"scope_id"`
	FileID    uuid.UUID            `json:"file_id"`
	ActorID   uuid.UUID            `json:"actor_id"`
	Input     UntrackedReviewInput `json:"input"`
	JobID     uuid.UUID            `json:"job_id"`
	CreatedAt time.Time            `json:"created_at"`
}

type UntrackedReviewRequestReceipt struct {
	CommandKey       string                 `json:"command_key"`
	Request          UntrackedReviewRequest `json:"request"`
	ExecutionAllowed bool                   `json:"execution_allowed"`
}

type UntrackedReviewUnit struct {
	UnitKey       string     `json:"unit_key"`
	TMDBEpisodeID int64      `json:"tmdb_episode_id"`
	Season        int        `json:"season"`
	Episode       int        `json:"episode"`
	AirDate       *time.Time `json:"air_date"`
}

type UntrackedReviewCandidate struct {
	Key           string                `json:"key"`
	Namespace     string                `json:"namespace"`
	TMDBID        int64                 `json:"tmdb_id"`
	Title         string                `json:"title"`
	OriginalTitle string                `json:"original_title"`
	Year          int                   `json:"year"`
	Units         []UntrackedReviewUnit `json:"units"`
}

// Evidence proposes identity and exact coverage; it never grants publication.
type UntrackedReviewEvidence struct {
	ParserVersion string                    `json:"parser_version"`
	ParsedTitle   string                    `json:"parsed_title"`
	ParsedYear    int                       `json:"parsed_year"`
	Selection     string                    `json:"selection"`
	Complete      bool                      `json:"complete"`
	Reason        string                    `json:"reason"`
	Candidate     *UntrackedReviewCandidate `json:"candidate"`
}

type UntrackedReviewSnapshot struct {
	Lease   JobLease
	Request UntrackedReviewRequest
	Scope   UntrackedScope
	File    UntrackedDiscoveredFile
}

type UntrackedReviewCapture interface {
	UntrackedReviewSnapshot() UntrackedReviewSnapshot
}

type UntrackedReview struct {
	Request          UntrackedReviewRequest   `json:"request"`
	State            string                   `json:"state"`
	Evidence         *UntrackedReviewEvidence `json:"evidence"`
	MaterialHash     string                   `json:"material_hash"`
	RecordedAt       *time.Time               `json:"recorded_at"`
	Stale            bool                     `json:"stale"`
	ExecutionAllowed bool                     `json:"execution_allowed"`
}

type UntrackedConfirmationInput struct {
	ReviewID           uuid.UUID `json:"review_id"`
	MaterialHash       string    `json:"material_hash"`
	ScopeVersion       int64     `json:"scope_version"`
	FileRevision       int64     `json:"file_revision"`
	CandidateKey       string    `json:"candidate_key"`
	Action             string    `json:"action"`
	TargetRelativePath string    `json:"target_relative_path"`
}

// Confirmation is durable intent. A later organization transaction needs its own authority.
type UntrackedConfirmationMaterial struct {
	ID        uuid.UUID                  `json:"id"`
	ActorID   uuid.UUID                  `json:"actor_id"`
	Scope     UntrackedScope             `json:"scope"`
	File      UntrackedDiscoveredFile    `json:"file"`
	Review    UntrackedReview            `json:"review"`
	Input     UntrackedConfirmationInput `json:"input"`
	CreatedAt time.Time                  `json:"created_at"`
}

type UntrackedConfirmationReceipt struct {
	CommandKey       string                        `json:"command_key"`
	Material         UntrackedConfirmationMaterial `json:"material"`
	State            string                        `json:"state"`
	ExecutionAllowed bool                          `json:"execution_allowed"`
}

type UntrackedConfirmationCapture interface {
	UntrackedConfirmationMaterial() UntrackedConfirmationMaterial
}
type UntrackedConfirmationPreparation struct {
	Receipt *UntrackedConfirmationReceipt
	Capture UntrackedConfirmationCapture
}

type UntrackedReviewRepository interface {
	RequestUntrackedReview(context.Context, *Actor, uuid.UUID, uuid.UUID, UntrackedReviewInput, string) (*UntrackedReviewRequestReceipt, error)
	ReadUntrackedReviewRequestReceipt(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (*UntrackedReviewRequestReceipt, error)
	ReadUntrackedReview(context.Context, *Actor, uuid.UUID, uuid.UUID, uuid.UUID) (*UntrackedReview, error)
	PrepareUntrackedConfirmation(context.Context, *Actor, uuid.UUID, uuid.UUID, UntrackedConfirmationInput, string) (*UntrackedConfirmationPreparation, error)
	CommitUntrackedConfirmation(context.Context, UntrackedConfirmationCapture) (*UntrackedConfirmationReceipt, error)
	ReadUntrackedConfirmationReceipt(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (*UntrackedConfirmationReceipt, error)
}

type UntrackedReviewExecutionRepository interface {
	LoadUntrackedReview(context.Context, JobLease) (UntrackedReviewCapture, error)
	ValidateUntrackedReview(context.Context, UntrackedReviewCapture) error
	CompleteUntrackedReview(context.Context, UntrackedReviewCapture, UntrackedReviewEvidence) error
	RecoverUntrackedReviews(context.Context, int) (int, error)
}
