package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type ScanMissingFile struct {
	FileID, FirstRunID, LastRunID                 uuid.UUID
	RelPath                                       string
	LastMutationSeq, MappingRevision, Size, Mtime int64
	Fingerprint                                   string
	FingerprintKnown                              bool
	ValidUntil                                    time.Time
}
type ScanMissingDecision string

const (
	ScanFilePresent ScanMissingDecision = "present"
	ScanFileMissing ScanMissingDecision = "missing"
)

// The caller holds the root physical slot and checks the marker and directory
// identity before and after Inspect. Only a definite NotExist permits Missing.
type ScanMissingRepository interface {
	LoadMissingScan(context.Context, uuid.UUID, uuid.UUID) (ScanSeal, error)
	ListMissingChecks(context.Context, ScanSeal, uuid.UUID) ([]ScanMissingFile, error)
	ResolveMissing(context.Context, ScanSeal, ScanMissingFile, ScanMissingDecision) error
}
