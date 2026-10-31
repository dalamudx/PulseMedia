package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// SeedingInspection is a transient native read, never a removal or scheduling authority.
type SeedingInspection struct {
	ObservedAt         time.Time `json:"observed_at"`
	Complete           bool      `json:"complete"`
	ShareRatio         *float64  `json:"share_ratio"`
	InfiniteShareRatio bool      `json:"infinite_share_ratio"`
	SeedingSeconds     *string   `json:"seeding_seconds"`
}

type PreparedSeedingInspection struct {
	Reader       SeedingReadClient
	InfoHash     string
	OperationTag string
	SavePath     string
	Validate     func(context.Context) error
	Release      func()
}

type SeedingInspectionRepository interface {
	PrepareSeedingInspection(context.Context, *Actor, uuid.UUID) (*PreparedSeedingInspection, error)
}
