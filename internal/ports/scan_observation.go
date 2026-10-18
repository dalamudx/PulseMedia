package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrScanObservationStale = errors.New("scan observation authority changed")

type ScanObservation struct {
	RunID, RootID, ObservationID, NewFileID                 uuid.UUID
	Path, MediaType, MarkerFilename                         string
	Generation, HealthRevision, ScanToken, StartMutationSeq int64
	RelPath                                                 string
	Identity                                                FileIdentity
}

type ScanObservationResult struct{ Added, Updated, Recorded bool }

// ScanObservationWriter commits a verified positive observation and any file
// incarnation replacement and source revocation atomically, without filesystem I/O.
type ScanObservationWriter interface {
	Record(context.Context, ScanObservation) (ScanObservationResult, error)
}
