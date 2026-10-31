package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// ManagedSidecarContent is authenticated only by an ordinary successful creation
// transaction or a completed explicit update, never by an existing path alone.
type ManagedSidecarContent struct {
	Name      string       `json:"name"`
	Directory string       `json:"directory"`
	Identity  FileIdentity `json:"identity"`
	Content   []byte       `json:"content"`
	Hash      string       `json:"hash"`
}
type ManagedSidecarArtifact struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	OldHash string `json:"old_hash,omitempty"`
	NewHash string `json:"new_hash,omitempty"`
}
type ManagedSidecarUpdateResult struct {
	ID         uuid.UUID                `json:"id"`
	FileID     uuid.UUID                `json:"file_id"`
	State      string                   `json:"state"`
	ExpiresAt  time.Time                `json:"expires_at"`
	Artifacts  []ManagedSidecarArtifact `json:"artifacts"`
	CommandKey string                   `json:"command_key,omitempty"`
}
type ManagedSidecarUpdateTicket struct {
	ID            uuid.UUID
	SourceJobID   uuid.UUID
	ConfigBinding string
	Snapshot      MediaSidecarSnapshot
	Managed       []ManagedSidecarContent
	Proposed      []ManagedSidecarContent
	ExpiresAt     time.Time
	Permit        uuid.UUID
}
type ManagedSidecarUpdateRepository interface {
	LoadManaged(context.Context, *Actor, uuid.UUID, uuid.UUID) (ManagedSidecarUpdateTicket, error)
	SaveManagedPreview(context.Context, *Actor, ManagedSidecarUpdateTicket, ManagedSidecarUpdateResult) (ManagedSidecarUpdateResult, error)
	AuthorizeManagedUpdate(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (ManagedSidecarUpdateTicket, ManagedSidecarUpdateResult, error)
	ValidateManagedUpdate(context.Context, *Actor, ManagedSidecarUpdateTicket) error
	CompleteManagedUpdate(context.Context, *Actor, ManagedSidecarUpdateTicket, ManagedSidecarUpdateResult, []ManagedSidecarContent) (ManagedSidecarUpdateResult, error)
	ReadManagedUpdate(context.Context, *Actor, uuid.UUID, string) (ManagedSidecarUpdateResult, error)
}

// The caller retains the physical root slot through calls and durable writeback.
// Backup names derive only from the immutable permit. Existing targets and backups
// are never overwritten. Changed/manual entries and retained backups require review.
type ManagedSidecarUpdateFilesystem interface {
	MediaSidecarFilesystem
	ReadManagedSidecar(context.Context, TransferRoot, SidecarRequest) (ManagedSidecarContent, error)
	ReplaceManagedSidecar(context.Context, TransferRoot, SidecarRequest, ManagedSidecarContent, uuid.UUID) (SidecarResult, error)
}
