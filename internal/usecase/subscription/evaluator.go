package subscription

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type EvaluationReport = ports.WantedProjection
type Evaluator struct {
	repo ports.WantedProjectionRepository
}

func NewEvaluator(repo ports.WantedProjectionRepository) *Evaluator { return &Evaluator{repo} }

// The policy reference is a fence, not permission to inject arbitrary rank maps.
// Rules are loaded from the immutable database revision within the narrow UoW.
func (e *Evaluator) EvaluateWantedItem(ctx context.Context, id uuid.UUID, policy quality.Policy) (*EvaluationReport, error) {
	return e.repo.ProjectWanted(ctx, id, policy.ID, policy.Revision)
}
