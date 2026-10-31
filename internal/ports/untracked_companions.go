package ports

import (
	"context"
	"github.com/google/uuid"
)

// Candidates are observations, not permission to publish. Selection revalidates them.
type CompanionCandidate struct {
	Key                string       `json:"key"`
	SourceRelativePath string       `json:"source_relative_path"`
	TargetRelativePath string       `json:"target_relative_path"`
	Identity           FileIdentity `json:"identity"`
}
type CompanionAttachment struct {
	ID             uuid.UUID          `json:"id"`
	OperationID    uuid.UUID          `json:"operation_id"`
	Candidate      CompanionCandidate `json:"candidate"`
	Action         string             `json:"action"`
	State          string             `json:"state"`
	SourceRetained bool               `json:"source_retained"`
}
type CompanionView struct {
	VideoState string                `json:"video_state"`
	Items      []CompanionAttachment `json:"items"`
}
type UntrackedCompanionSelection struct {
	CandidateKeys []string `json:"candidate_keys"`
	Action        string   `json:"action"`
}
type CompanionDirectory interface {
	ListCompanionDirectory(context.Context, TransferRoot, string) ([]UntrackedFileObservation, error)
}
type CompanionExecution struct {
	Attachment CompanionAttachment
	Owner      uuid.UUID
	Token      int64
	Content    *TransferContent
}

// All write methods recheck the current actor, operation, configuration and frozen intent.
type UntrackedCompanionRepository interface {
	CompanionMaterial(context.Context, *Actor, uuid.UUID) (UntrackedOrganizationMaterial, error)
	ReadCompanions(context.Context, *Actor, uuid.UUID) (*CompanionView, error)
	ReplayCompanionSelection(context.Context, *Actor, uuid.UUID, string, string) (*CompanionView, error)
	SelectCompanions(context.Context, *Actor, uuid.UUID, string, string, []CompanionCandidate, string) (*CompanionView, error)
	ClaimCompanion(context.Context, *Actor, uuid.UUID, uuid.UUID) (*CompanionExecution, error)
	SealCompanion(context.Context, *Actor, CompanionExecution, TransferContent) error
	ValidateCompanion(context.Context, *Actor, CompanionExecution) error
	FinishCompanion(context.Context, *Actor, CompanionExecution, *VerifiedTransferResult) error
}
