package ports

import (
	"context"
	"github.com/google/uuid"
)

const SubtitleCandidateJobKind = "media_subtitle_candidates"

type SubtitleCandidateSummary struct {
	CueCount int   `json:"cue_count"`
	StartMS  int64 `json:"start_ms"`
	EndMS    int64 `json:"end_ms"`
}
type SubtitleCandidate struct {
	ID           uuid.UUID                 `json:"id"`
	Provider     string                    `json:"provider"`
	LanguageHint string                    `json:"language_hint"`
	Format       string                    `json:"format"`
	Summary      *SubtitleCandidateSummary `json:"summary,omitempty"`
}
type SubtitleCandidateState struct {
	FileID          uuid.UUID           `json:"file_id"`
	MappingRevision int64               `json:"mapping_revision"`
	QueryID         *uuid.UUID          `json:"query_id,omitempty"`
	Outcome         string              `json:"outcome"`
	Candidates      []SubtitleCandidate `json:"candidates"`
}

// Candidate providers return bounded validated bytes. Signed retrieval URLs remain
// local to the adapter and never enter database rows or public API values.
type SubtitleCandidateContent struct{ Content []byte }
type SubtitleCandidateProvider interface {
	FindSubtitleCandidates(context.Context, SubtitleVideoHash) ([]SubtitleCandidateContent, error)
}
type SubtitleCandidateTicket struct {
	QueryID  uuid.UUID
	ActorID  uuid.UUID
	Lease    JobLease
	Snapshot MediaSidecarSnapshot
}
type SubtitleCandidateRepository interface {
	ReadCandidates(context.Context, *Actor, uuid.UUID) (SubtitleCandidateState, error)
	BeginCandidateQuery(context.Context, *Actor, uuid.UUID, int64, string) (SubtitleCandidateTicket, SubtitleCandidateState, error)
	ValidateCandidate(context.Context, SubtitleCandidateTicket) error
	FinishCandidateQuery(context.Context, SubtitleCandidateTicket, string, []SubtitleCandidateContent) (SubtitleCandidateState, error)
	BeginCandidateConfirmation(context.Context, *Actor, uuid.UUID, uuid.UUID, int64) (SubtitleCandidateTicket, SubtitleCandidateState, []byte, error)
	FinishCandidateConfirmation(context.Context, SubtitleCandidateTicket, MediaSubtitleCompletion) (SubtitleCandidateState, error)
}
