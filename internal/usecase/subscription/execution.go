package subscription

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type ExecutionService struct {
	repo ports.SubscriptionExecutionRepository
}

func NewExecutionService(repo ports.SubscriptionExecutionRepository) *ExecutionService {
	return &ExecutionService{repo: repo}
}
func (s *ExecutionService) Read(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.SubscriptionExecution, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if id == uuid.Nil {
		return nil, ports.ErrNotFound
	}
	out, err := s.repo.ReadSubscriptionExecution(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, ports.ErrUnavailable
	}
	for i := range out.Rounds {
		r := &out.Rounds[i]
		r.Reason = executionReason(r.Reason)
		if len(r.Candidates) > 100 {
			r.Candidates = r.Candidates[:100]
			r.Limited = true
		}
	}
	if len(out.Rounds) > 20 {
		out.Rounds = out.Rounds[:20]
		out.Limited = true
	}
	if out.Waiting == "" {
		out.Waiting = "no_round"
		out.NextStep = "wait_for_next_evaluation"
		if len(out.Rounds) > 0 {
			r := out.Rounds[0]
			switch {
			case r.State == "running" && r.Deadline.After(out.ObservedAt):
				out.Waiting = "source_results"
				out.NextStep = "wait_for_persisted_results"
			case r.State == "running":
				out.Waiting = "finalization"
				out.NextStep = "wait_for_worker"
			default:
				out.Waiting = "round_finished"
				out.NextStep = "check_wanted_status"
			}
		}
	}
	return out, nil
}
func executionReason(v string) string {
	// Persisted reasons from older adapters may contain arbitrary text. Only bounded codes leave this boundary.
	switch v {
	case "selected", "no_qualified_candidate", "owner_revoked", "target_changed", "retry_exhausted", "deadline", "no_candidates", "intent_changed", "policy_changed", "root_unavailable", "candidate_unavailable", "local_reuse_required", "already_owned", "reserved", "attached":
		return v
	}
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return "review_required"
}
