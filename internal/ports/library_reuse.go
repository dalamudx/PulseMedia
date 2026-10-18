package ports

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
)

var ErrInvalidLibraryReuse = errors.New("invalid library reuse request")
var ErrLibraryReuseChanged = errors.New("library reuse inputs changed")

// LibraryReuseTarget is a demand for ownership in one particular root. Files in
// other roots are possible sources, never proof that this target is owned.
type LibraryReuseTarget struct {
	UnitID         uuid.UUID `json:"unit_id"`
	RootID         uuid.UUID `json:"root_id"`
	SubscriptionID uuid.UUID `json:"subscription_id,omitempty"`
	PolicyID       string    `json:"policy_id"`
	PolicyRevision int64     `json:"policy_revision"`
}

type LibraryReuseCandidate struct {
	FileID          uuid.UUID       `json:"file_id"`
	RootID          uuid.UUID       `json:"root_id"`
	RootName        string          `json:"root_name"`
	RelPath         string          `json:"rel_path"`
	UnitIDs         []uuid.UUID     `json:"unit_ids"`
	UnitKeys        []string        `json:"unit_keys"`
	Quality         quality.Profile `json:"quality"`
	Score           int             `json:"score"`
	SourceAvailable bool            `json:"source_available"`
	Qualified       bool            `json:"qualified"`
	WaitReason      string          `json:"wait_reason"`
}

type LibraryReusePreview struct {
	Target           LibraryReuseTarget      `json:"target"`
	MediaID          uuid.UUID               `json:"media_id"`
	MediaType        string                  `json:"media_type"`
	UnitKey          string                  `json:"unit_key"`
	AlreadyOwned     bool                    `json:"already_owned"`
	OwnershipUnknown bool                    `json:"ownership_unknown"`
	Complete         bool                    `json:"complete"`
	Candidates       []LibraryReuseCandidate `json:"candidates"`
	InputStamp       string                  `json:"input_stamp"`
	CanConfirm       bool                    `json:"can_confirm"`
	WaitReason       string                  `json:"wait_reason"`
}

type LibraryReusePreviewRepository interface {
	PreviewLibraryReuseBatch(context.Context, *Actor, []LibraryReuseTarget) ([]LibraryReusePreview, error)
}

// Confirmation explicitly accepts the complete file mapping, including units not
// present in the original request. It cannot expand or silently trim that set.
type LibraryReuseCommand struct {
	Target           LibraryReuseTarget `json:"target"`
	SourceFileID     uuid.UUID          `json:"source_file_id"`
	ConfirmedUnitIDs []uuid.UUID        `json:"confirmed_unit_ids"`
	TargetRel        string             `json:"target_rel"`
	Action           string             `json:"action"`
	InputStamp       string             `json:"input_stamp"`
	Confirmed        bool               `json:"confirmed"`
}

type LibraryReuseReceipt struct {
	PlanID uuid.UUID `json:"plan_id"`
	JobID  uuid.UUID `json:"job_id"`
	State  string    `json:"state"`
}

type LibraryReuseStatus struct {
	LibraryReuseReceipt
	Reason       string      `json:"reason"`
	RootID       uuid.UUID   `json:"root_id"`
	SourceFileID uuid.UUID   `json:"source_file_id"`
	TargetRel    string      `json:"target_rel"`
	Action       string      `json:"action"`
	UnitIDs      []uuid.UUID `json:"unit_ids"`
	TargetFileID *uuid.UUID  `json:"target_file_id,omitempty"`
}

type LibraryReuseRepository interface {
	LibraryReusePreviewRepository
	ConfirmLibraryReuse(context.Context, *Actor, LibraryReuseCommand, string) (*LibraryReuseReceipt, error)
	ListLibraryReuse(context.Context, *Actor) ([]LibraryReuseStatus, error)
	ExecuteLibraryReuse(context.Context, JobLease) error
}
