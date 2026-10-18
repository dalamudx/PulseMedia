package ports

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
)

var (
	ErrAcquisitionCommandConflict     = errors.New("request key belongs to a different acquisition command")
	ErrAcquisitionUnsupportedCoverage = errors.New("only validated single movie coverage is currently supported")
	ErrAcquisitionOwnershipUncertain  = errors.New("target ownership evidence is unknown; review required")
	ErrAcquisitionAlreadyOwned        = errors.New("media unit is already owned in target library root")
	ErrAcquisitionRootNotOnline       = errors.New("target library root is offline or unreachable")
	ErrAcquisitionLocalReuseRequired  = errors.New("existing file in another root requires local reuse")
	ErrAcquisitionUnitAlreadyReserved = errors.New("media unit is already reserved for acquisition")
)

type AcquireMovieCommand struct {
	UnitIDs                                   []uuid.UUID `json:"unit_ids"` // exactly one validated movie; TV/pack requires a separate frozen-coverage UoW
	RootID                                    uuid.UUID   `json:"root_id"`
	CandidateID                               uuid.UUID   `json:"candidate_id"`
	DownloaderID                              *uuid.UUID  `json:"downloader_id,omitempty"`
	IdempotencyKey                            string      `json:"idempotency_key,omitempty"`
	SubscriptionID                            *uuid.UUID  `json:"-"`
	IntentVersion                             int64       `json:"-"`
	PolicyID                                  string      `json:"-"`
	PolicyRevision                            int64       `json:"-"`
	UnitMetadataVersion, MediaMetadataVersion int64       `json:"-"`
}

type AcquireResult struct {
	OperationID       uuid.UUID   `json:"operation_id"`
	Reservations      []uuid.UUID `json:"reservations"`
	JobID             uuid.UUID   `json:"job_id"`
	Status            string      `json:"status"`
	SourceCandidateID uuid.UUID   `json:"source_candidate_id"`
}

var ErrAcquisitionRoundAborted = errors.New("search round snapshot no longer authoritative")
var ErrAcquisitionRoundNotReady = errors.New("search round still awaiting executions")

type RoundSnapshot struct {
	UnitMetadataVersion    int64             `json:"unit_metadata_version"`
	MediaMetadataVersion   int64             `json:"media_metadata_version"`
	PolicyID               string            `json:"policy_id"`
	RootID                 uuid.UUID         `json:"root_id"`
	RootGeneration         int64             `json:"root_generation"`
	RootPath               string            `json:"root_path"`
	UnitID                 uuid.UUID         `json:"unit_id"`
	MediaID                uuid.UUID         `json:"media_id"`
	Title                  string            `json:"title"`
	Year                   int32             `json:"year"`
	Identities             map[string]string `json:"identities"`
	Indexers               map[string]int64  `json:"indexers"`
	Bindings               json.RawMessage   `json:"bindings"`
	DownloaderID           *uuid.UUID        `json:"downloader_id"`
	SelectedDownloaderID   *uuid.UUID        `json:"selected_downloader_id"`
	DownloaderConfigDigest string            `json:"downloader_config_digest"`
	SharedSource           bool              `json:"shared_source"`
	Scheduled              bool              `json:"scheduled"`
}
type RoundDecision struct {
	RoundID             uuid.UUID
	OperationID         uuid.UUID
	State               string
	SelectedCandidateID *uuid.UUID
	BestScore           int
	Reason              string
}
type RoundJobAuthority struct {
	ID    uuid.UUID
	Owner string
	Token int64
}

type AcquisitionRepository interface {
	AcquireMovie(context.Context, *Actor, AcquireMovieCommand) (*AcquireResult, error)
	FinalizeMovieRound(context.Context, *Actor, uuid.UUID, *RoundJobAuthority) (*RoundDecision, error)
	PreSubmitRecheck(context.Context, uuid.UUID) (bool, error)
}
