package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AutomaticTVExecutionAuthority is produced only by the leased atomic round
// finalizer. It is independent of administrator-reviewed episode permissions.
// SendValidUntil bounds a first send; expiry never proves an unknown Add absent.
type AutomaticTVExecutionAuthority struct {
	SchemaVersion       string                   `json:"schema_version"`
	Kind                string                   `json:"kind"`
	OperationID         uuid.UUID                `json:"operation_id"`
	RoundID             uuid.UUID                `json:"round_id"`
	RoundSnapshotHash   string                   `json:"round_snapshot_hash"`
	Target              AutomaticTVRoundSnapshot `json:"target"`
	RootPathDigest      string                   `json:"root_path_digest"`
	QueryID             uuid.UUID                `json:"query_id"`
	QueryParametersHash string                   `json:"query_parameters_hash"`
	CandidateID         uuid.UUID                `json:"candidate_id"`
	IndexerID           uuid.UUID                `json:"indexer_id"`
	SourceConfigVersion int64                    `json:"source_config_version"`
	SourceStamp         string                   `json:"source_stamp"`
	DecisionStamp       string                   `json:"decision_stamp"`
	IssuedAt            time.Time                `json:"issued_at"`
	SendValidUntil      time.Time                `json:"send_valid_until"`
}

type AutomaticTVAcquisitionRepository interface {
	FinalizeAutomaticTVRound(context.Context, uuid.UUID, JobLease) (*RoundDecision, error)
	RecoverAutomaticTVFinalizers(context.Context, int32) (int, error)
}
