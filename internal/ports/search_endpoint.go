package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// SearchEndpointTask binds an explicit lease to the persisted round endpoint.
type SearchEndpointTask struct {
	Lease     JobLease  `json:"-"`
	RoundID   uuid.UUID `json:"round_id"`
	IndexerID uuid.UUID `json:"indexer_id"`
}
type SearchEndpointScope struct {
	Task          SearchEndpointTask
	ConfigVersion int64
	Title         string
	Year          int32
	Deadline      time.Time
	SnapshotHash  [32]byte
	MaySearch     bool
	NextPage      int32
	SavedQueries  []uuid.UUID
	PageTotal     *int64
	SeenGUIDs     []string
}
type SearchEndpointRepository interface {
	Load(context.Context, SearchEndpointTask) (SearchEndpointScope, error)
	Complete(context.Context, SearchEndpointScope, uuid.UUID) error
}
