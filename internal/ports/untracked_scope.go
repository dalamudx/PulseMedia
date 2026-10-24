package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// An enabled scope authorizes discovery and review, never file publication.
type UntrackedScopeInput struct {
	Enabled                bool      `json:"enabled"`
	ExpectedVersion        int64     `json:"expected_version"`
	ClientID               uuid.UUID `json:"client_id"`
	ExpectedClientVersion  time.Time `json:"expected_client_version"`
	RemotePrefix           string    `json:"remote_prefix"`
	TargetRootID           uuid.UUID `json:"target_root_id"`
	ExpectedRootGeneration int64     `json:"expected_root_generation"`
}

type UntrackedScope struct {
	ID                   uuid.UUID    `json:"id"`
	OwnerID              uuid.UUID    `json:"owner_id"`
	Enabled              bool         `json:"enabled"`
	Version              int64        `json:"version"`
	ClientID             uuid.UUID    `json:"client_id"`
	ClientVersion        time.Time    `json:"client_version"`
	ClientStamp          string       `json:"client_stamp"`
	RemotePrefix         string       `json:"remote_prefix"`
	LocalRoot            string       `json:"local_root"`
	SourceIdentity       FileIdentity `json:"source_identity"`
	TargetRootID         uuid.UUID    `json:"target_root_id"`
	TargetRootPath       string       `json:"target_root_path"`
	TargetRootGeneration int64        `json:"target_root_generation"`
	TargetMediaType      string       `json:"target_media_type"`
	TargetMarker         string       `json:"target_marker"`
	TargetBaselineScanID uuid.UUID    `json:"target_baseline_scan_id"`
	TargetIdentity       FileIdentity `json:"target_identity"`
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
}

type UntrackedScopeReceipt struct {
	CommandKey       string         `json:"command_key"`
	Scope            UntrackedScope `json:"scope"`
	ExecutionAllowed bool           `json:"execution_allowed"`
}

// Issued by the repository only after current role, key and selection checks.
type UntrackedScopeCapture interface {
	UntrackedScopeMaterial() UntrackedScopeMaterial
}

type UntrackedScopeMaterial struct {
	ScopeID              uuid.UUID
	OwnerID              uuid.UUID
	ExpectedVersion      int64
	Enabled              bool
	ClientID             uuid.UUID
	ClientVersion        time.Time
	ClientStamp          string
	RemotePrefix         string
	LocalRoot            string
	SourceIdentity       FileIdentity
	TargetRoot           ScanRootSnapshot
	TargetBaselineScanID uuid.UUID
	TargetIdentity       FileIdentity
}

type UntrackedScopePreparation struct {
	Receipt *UntrackedScopeReceipt
	Capture UntrackedScopeCapture
}

type UntrackedScopeEvidence struct {
	SourceIdentity FileIdentity
	TargetIdentity FileIdentity
}

type UntrackedScopeRepository interface {
	ReadUntrackedScope(context.Context, *Actor, uuid.UUID) (*UntrackedScope, error)
	PrepareUntrackedScope(context.Context, *Actor, uuid.UUID, UntrackedScopeInput, string) (*UntrackedScopePreparation, error)
	CommitUntrackedScope(context.Context, UntrackedScopeCapture, UntrackedScopeEvidence) (*UntrackedScopeReceipt, error)
	ReadUntrackedScopeReceipt(context.Context, *Actor, uuid.UUID, string) (*UntrackedScopeReceipt, error)
}
