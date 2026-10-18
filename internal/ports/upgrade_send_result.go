package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// MovieUpgradeSendReturnProof is opaque evidence inspected by the trusted
// verifier. Implementing a method or supplying a boolean grants no authority.
type MovieUpgradeSendReturnProof any

// MovieUpgradeSendReturnBinding identifies one original physical attempt.
// Digests are lowercase hexadecimal SHA256 values.
type MovieUpgradeSendReturnBinding struct {
	OperationID uuid.UUID
	TaskID      uuid.UUID
	Owner       uuid.UUID
	Token       uuid.UUID
	ArmSHA256   string
	RawSHA256   string
}

// MovieUpgradeSendVerifiedReturn is produced by a trusted verifier only after
// validating evidence that the original synchronous invocation returned.
//
// Status is exactly "confirmed" or "unknown". Unknown includes an ambiguous
// external outcome after a real synchronous return. A timeout notification
// while the invocation is still executing is not return evidence.
type MovieUpgradeSendVerifiedReturn struct {
	Binding    MovieUpgradeSendReturnBinding
	Status     string
	ReturnedAt time.Time
}

// Implementations are trusted dependencies, not request-controlled values.
//
// Verify must validate independently issued evidence and its binding. It must
// reject nil, zero values, caller-authored assertions and proofs from another
// invocation. It must not merely copy expected into its return value.
//
// A native Manager issues private call-scoped evidence after synchronous return.
// The verifier and client budget belong to the same owned physical attempt.
// Verification errors must not contain external errors, URLs or metainfo.
type MovieUpgradeSendReturnVerifier interface {
	VerifyMovieUpgradeSendReturn(
		context.Context,
		MovieUpgradeSendReturnProof,
		MovieUpgradeSendReturnBinding,
	) (MovieUpgradeSendVerifiedReturn, error)
}

// A historical result never authorizes another physical invocation.
type MovieUpgradeSendResultReceipt struct {
	OperationID      uuid.UUID `json:"operation_id"`
	TaskID           uuid.UUID `json:"task_id"`
	Owner            uuid.UUID `json:"owner"`
	Token            uuid.UUID `json:"token"`
	Status           string    `json:"status"`
	ReturnedAt       time.Time `json:"returned_at"`
	RecordedAt       time.Time `json:"recorded_at"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
