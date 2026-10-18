package ports

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
)

// EvaluationScheduleWanted contains only the scheduling facts of a wanted item.
type EvaluationScheduleWanted struct {
	ID             uuid.UUID
	SubscriptionID uuid.UUID
	NextEvaluateAt *time.Time
}

type EvaluationScheduleSubscription struct {
	UserID                uuid.UUID
	QualityPolicyID       string
	QualityPolicyRevision int64
}

// EvaluationScheduleRepository retains database-clock retry and RSS watermarks.
// Starting a round and its jobs remains the coordinator's independent narrow UoW.
type EvaluationScheduleRepository interface {
	ListPendingEvaluations(context.Context, int32) ([]EvaluationScheduleWanted, error)
	LoadEvaluationSubscription(context.Context, uuid.UUID) (EvaluationScheduleSubscription, error)
	EvaluationDatabaseTime(context.Context) (time.Time, error)
	HasFreshEvaluationRSS(context.Context, uuid.UUID) (bool, error)
	ConsumeEvaluationRSS(context.Context, uuid.UUID, time.Time) error
	HasActiveEvaluationRound(context.Context, uuid.UUID) (bool, error)
	RescheduleEvaluation(context.Context, uuid.UUID, time.Duration) error
}

type ScheduleWantedEvaluator interface {
	EvaluateWantedItem(context.Context, uuid.UUID, quality.Policy) (*WantedProjection, error)
}

type SchedulePolicyReader interface {
	GetRevision(context.Context, string, int64) (*quality.PolicyRules, error)
}

type ScheduledRoundCoordinator interface {
	ScheduleAutomaticRound(context.Context, uuid.UUID, time.Duration) error
	ScheduleSharedRound(context.Context, uuid.UUID) error
	FinalizeScheduledRound(context.Context, uuid.UUID, RoundJobAuthority) error
}
