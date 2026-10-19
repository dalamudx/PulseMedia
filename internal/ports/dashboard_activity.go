package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Dashboard activity is a read-only projection of visible operation records.
// Progress describes stored downloader telemetry, never ownership or ingestion.
type DashboardActivity struct {
	OperationID    uuid.UUID  `json:"operation_id"`
	Title          string     `json:"title"`
	Status         string     `json:"status"`
	Progress       *float64   `json:"progress,omitempty"`
	ProgressSource string     `json:"progress_source,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
}

type DashboardActivityPage struct {
	Items      []DashboardActivity `json:"items"`
	More       bool                `json:"more"`
	ObservedAt time.Time           `json:"observed_at"`
}

type DashboardActivityReader interface {
	ReadDashboardActivity(context.Context, *Actor, int) (*DashboardActivityPage, error)
}
