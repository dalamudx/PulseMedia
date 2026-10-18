package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrWantedProjectionChanged = errors.New("wanted projection inputs changed")

type WantedProjection struct {
	WantedItemID, UnitID uuid.UUID
	IsSatisfied          bool
	FactStage            *string
	NextEvaluateAt       *time.Time
	WaitReason           string
	Score                int
}

// ProjectWanted guards policy -> subscription -> root -> unit -> wanted and
// commits only a result derived from those current locked inputs. No I/O or tx escapes.
type WantedProjectionRepository interface {
	ProjectWanted(context.Context, uuid.UUID, string, int64) (*WantedProjection, error)
}
