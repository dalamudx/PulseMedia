package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrLibraryMaintenanceChanged = errors.New("library maintenance evidence changed")

type MaintenanceFileObservation struct {
	Identity       FileIdentity `json:"identity"`
	AllocatedBytes int64        `json:"allocated_bytes"`
	LinkCount      int64        `json:"link_count"`
}

// These primitives accept capabilities, never absolute paths. The caller retains
// the root physical lock through the actual call and durable writeback.
type LibraryMaintenanceFilesystem interface {
	RootFilesystem
	MaintenanceInspect(context.Context, TransferRoot, string) (MaintenanceFileObservation, error)
	MaintenanceRename(context.Context, TransferRoot, string, string, FileIdentity) error
	MaintenanceRemove(context.Context, TransferRoot, string, FileIdentity) error
}
type LibraryReorganizationRequest struct {
	FileID             uuid.UUID `json:"file_id"`
	TargetRelativePath string    `json:"target_relative_path"`
}
type LibraryReorganizationPreview struct {
	FileID             uuid.UUID `json:"file_id"`
	RootID             uuid.UUID `json:"root_id"`
	SourceRelativePath string    `json:"source_relative_path"`
	TargetRelativePath string    `json:"target_relative_path"`
	Version            string    `json:"version"`
	CanConfirm         bool      `json:"can_confirm"`
	ExecutionAllowed   bool      `json:"execution_allowed"`
}
type LibraryRecyclePreviewRequest struct {
	RecycleID string `json:"recycle_id"`
	Action    string `json:"action"`
}
type LibraryMaintenanceCommand struct {
	Action             string    `json:"action"`
	FileID             uuid.UUID `json:"file_id,omitempty,omitzero"`
	TargetRelativePath string    `json:"target_relative_path,omitempty"`
	RecycleID          string    `json:"recycle_id,omitempty"`
	ExpectedVersion    string    `json:"expected_version"`
	Confirmed          bool      `json:"confirmed"`
}
type LibraryMaintenanceResult struct {
	CommandID        uuid.UUID `json:"command_id"`
	Action           string    `json:"action"`
	Status           string    `json:"status"`
	FileID           uuid.UUID `json:"file_id"`
	Version          string    `json:"version"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type LibraryRecycleItem struct {
	RecycleID            string     `json:"recycle_id"`
	Kind                 string     `json:"kind"`
	OldFileID            uuid.UUID  `json:"old_file_id"`
	ReplacementFileID    uuid.UUID  `json:"replacement_file_id"`
	RootID               uuid.UUID  `json:"root_id"`
	OriginalRelativePath string     `json:"original_relative_path"`
	RetainedUntil        time.Time  `json:"retained_until"`
	Version              string     `json:"version"`
	Status               string     `json:"status"`
	SizeBytes            int64      `json:"size_bytes"`
	AllocatedBytes       *int64     `json:"allocated_bytes"`
	LinkCount            *int64     `json:"link_count"`
	UsageObservedAt      *time.Time `json:"usage_observed_at"`
	CanConfirm           bool       `json:"can_confirm"`
	ExecutionAllowed     bool       `json:"execution_allowed"`
}
type LibraryRecycleList struct {
	Items      []LibraryRecycleItem `json:"items"`
	NextCursor string               `json:"next_cursor"`
}
type LibraryRecycleQuery struct {
	Cursor string
	Limit  int
	Kind   string
	Query  string
}
type LibraryMaintenanceRepository interface {
	PreviewReorganization(context.Context, *Actor, LibraryReorganizationRequest) (*LibraryReorganizationPreview, error)
	PreviewRecycle(context.Context, *Actor, LibraryRecyclePreviewRequest) (*LibraryRecycleItem, error)
	PrepareCommand(context.Context, *Actor, LibraryMaintenanceCommand, string) (PreparedLibraryMaintenance, error)
	ReadCommand(context.Context, *Actor, string) (*LibraryMaintenanceResult, error)
	ListRecycle(context.Context, *Actor, LibraryRecycleQuery) (*LibraryRecycleList, error)
}

// PreparedLibraryMaintenance owns the original permanent permit and root/session
// capabilities. A recovered receipt never carries physical execution authority.
type PreparedLibraryMaintenance interface {
	PreviousResult() *LibraryMaintenanceResult
	BeforeCall(context.Context) error
	Remove(context.Context) error
	Rename(context.Context) error
	Observe(context.Context) error
	Complete(context.Context, bool) (*LibraryMaintenanceResult, error)
	Close()
}
