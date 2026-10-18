package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type AutoFulfillScheduler struct {
	repo      ports.EvaluationScheduleRepository
	evaluator ports.ScheduleWantedEvaluator
	coord     ports.ScheduledRoundCoordinator
	polSvc    ports.SchedulePolicyReader
}

func NewAutoFulfillScheduler(repo ports.EvaluationScheduleRepository, evaluator ports.ScheduleWantedEvaluator, coord ports.ScheduledRoundCoordinator, policies ports.SchedulePolicyReader) *AutoFulfillScheduler {
	return &AutoFulfillScheduler{repo: repo, evaluator: evaluator, coord: coord, polSvc: policies}
}

// RunEvaluationBatch only evaluates projections and atomically queues bounded
// network jobs. No external I/O runs on a scheduler tick.
func (s *AutoFulfillScheduler) RunEvaluationBatch(ctx context.Context, actor *ports.Actor, batchSize int32) (int, error) {
	if actor == nil {
		return 0, ports.ErrUnauthorized
	}
	if batchSize <= 0 || batchSize > 100 {
		batchSize = 20
	}
	items, err := s.repo.ListPendingEvaluations(ctx, batchSize)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, wanted := range items {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		sub, e := s.repo.LoadEvaluationSubscription(ctx, wanted.SubscriptionID)
		if e != nil {
			return processed, e
		}
		if actor.Type != ports.ActorTypeSystem && actor.Role != ports.RoleAdmin && actor.ID != sub.UserID.String() {
			continue
		}
		observed, e := s.repo.EvaluationDatabaseTime(ctx)
		if e != nil {
			return processed, e
		}
		freshRSS, e := s.repo.HasFreshEvaluationRSS(ctx, wanted.ID)
		if e != nil {
			return processed, e
		}
		rules, e := s.polSvc.GetRevision(ctx, sub.QualityPolicyID, sub.QualityPolicyRevision)
		if e != nil {
			return processed, e
		}
		report, e := s.evaluator.EvaluateWantedItem(ctx, wanted.ID, quality.Policy{ID: sub.QualityPolicyID, Revision: sub.QualityPolicyRevision, ResolutionOrder: rules.ResolutionOrder, SourceOrder: rules.SourceOrder, PreferHDR: rules.PreferHDR})
		if errors.Is(e, ports.ErrWantedProjectionChanged) {
			continue
		}
		if e != nil {
			return processed, e
		}
		processed++
		eligible := report.WaitReason == "missing" || report.WaitReason == "file_rejected" || report.WaitReason == "no_qualified_candidate"
		if !eligible || report.IsSatisfied {
			if e = s.repo.ConsumeEvaluationRSS(ctx, wanted.ID, observed); e != nil {
				return processed, e
			}
		}
		if report.WaitReason == "shared_acquisition_available" {
			if e = s.coord.ScheduleSharedRound(ctx, wanted.ID); e != nil && ctx.Err() != nil {
				return processed, ctx.Err()
			}
			continue
		}
		if report.IsSatisfied || !eligible {
			continue
		}
		// Round/input invalidation still refreshes ownership, without bypassing retry time.
		if report.WaitReason == "no_qualified_candidate" && wanted.NextEvaluateAt != nil && wanted.NextEvaluateAt.After(observed) && !freshRSS {
			continue
		}
		active, e := s.repo.HasActiveEvaluationRound(ctx, wanted.ID)
		if e != nil {
			return processed, e
		}
		if active {
			if e = s.repo.RescheduleEvaluation(ctx, wanted.ID, 30*time.Second); e != nil {
				return processed, e
			}
			continue
		}
		if e = s.coord.ScheduleAutomaticRound(ctx, wanted.ID, 30*time.Second); e != nil {
			if e = s.repo.ConsumeEvaluationRSS(ctx, wanted.ID, observed); e != nil {
				return processed, e
			}
			// Failed prerequisites and ambiguous/TV inputs wait; never invent a round.
			if ctx.Err() != nil {
				return processed, ctx.Err()
			}
			if e = s.repo.RescheduleEvaluation(ctx, wanted.ID, 5*time.Minute); e != nil {
				return processed, e
			}
		}
	}
	return processed, nil
}

func (s *AutoFulfillScheduler) FinalizeJob(ctx context.Context, id uuid.UUID, authority ports.RoundJobAuthority) error {
	err := s.coord.FinalizeScheduledRound(ctx, id, authority)
	if errors.Is(err, ports.ErrAcquisitionRoundAborted) {
		return nil
	}
	return err
}

// Legacy in-memory polling stays disabled. Durable RSS is handled by the App.
func (s *AutoFulfillScheduler) PollRSS(context.Context, uuid.UUID, ports.IndexerClient) (int, error) {
	return 0, ports.ErrUnavailable
}
