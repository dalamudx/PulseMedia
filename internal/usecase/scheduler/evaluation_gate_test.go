package scheduler_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	"github.com/google/uuid"
)

type evaluationGate struct {
	now                                       time.Time
	wanted                                    ports.EvaluationScheduleWanted
	sub                                       ports.EvaluationScheduleSubscription
	projection                                ports.WantedProjection
	fresh, active                             bool
	projectionErr, activeErr                  error
	evaluated, activeReads, automatic, shared int
	consumed                                  []time.Time
	rescheduled                               []time.Duration
	window                                    time.Duration
}

func (g *evaluationGate) ListPendingEvaluations(context.Context, int32) ([]ports.EvaluationScheduleWanted, error) {
	return []ports.EvaluationScheduleWanted{g.wanted}, nil
}
func (g *evaluationGate) LoadEvaluationSubscription(context.Context, uuid.UUID) (ports.EvaluationScheduleSubscription, error) {
	return g.sub, nil
}
func (g *evaluationGate) EvaluationDatabaseTime(context.Context) (time.Time, error) {
	return g.now, nil
}
func (g *evaluationGate) HasFreshEvaluationRSS(context.Context, uuid.UUID) (bool, error) {
	return g.fresh, nil
}
func (g *evaluationGate) ConsumeEvaluationRSS(_ context.Context, _ uuid.UUID, at time.Time) error {
	g.consumed = append(g.consumed, at)
	return nil
}
func (g *evaluationGate) HasActiveEvaluationRound(context.Context, uuid.UUID) (bool, error) {
	g.activeReads++
	return g.active, g.activeErr
}
func (g *evaluationGate) RescheduleEvaluation(_ context.Context, _ uuid.UUID, delay time.Duration) error {
	g.rescheduled = append(g.rescheduled, delay)
	return nil
}
func (g *evaluationGate) GetRevision(context.Context, string, int64) (*quality.PolicyRules, error) {
	return &quality.PolicyRules{}, nil
}
func (g *evaluationGate) EvaluateWantedItem(context.Context, uuid.UUID, quality.Policy) (*ports.WantedProjection, error) {
	g.evaluated++
	return &g.projection, g.projectionErr
}
func (g *evaluationGate) ScheduleAutomaticRound(_ context.Context, _ uuid.UUID, window time.Duration) error {
	g.automatic++
	g.window = window
	return nil
}
func (g *evaluationGate) ScheduleSharedRound(context.Context, uuid.UUID) error {
	g.shared++
	return nil
}
func (g *evaluationGate) FinalizeScheduledRound(context.Context, uuid.UUID, ports.RoundJobAuthority) error {
	return nil
}

func newEvaluationGate() *evaluationGate {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	next := now.Add(time.Hour)
	return &evaluationGate{
		now:        now,
		wanted:     ports.EvaluationScheduleWanted{ID: uuid.New(), SubscriptionID: uuid.New(), NextEvaluateAt: &next},
		sub:        ports.EvaluationScheduleSubscription{UserID: uuid.New(), QualityPolicyID: "default", QualityPolicyRevision: 1},
		projection: ports.WantedProjection{WaitReason: "no_qualified_candidate"},
	}
}

func TestEvaluationRetryUsesDatabaseTimeAndFreshRSS(t *testing.T) {
	for _, name := range []string{"retry_not_due", "retry_at_database_now", "new_rss_before_retry"} {
		t.Run(name, func(t *testing.T) {
			gate := newEvaluationGate()
			switch name {
			case "retry_at_database_now":
				gate.wanted.NextEvaluateAt = &gate.now
			case "new_rss_before_retry":
				gate.fresh = true
			}
			service := scheduler.NewAutoFulfillScheduler(gate, gate, gate, gate)
			n, err := service.RunEvaluationBatch(context.Background(), &ports.Actor{Type: ports.ActorTypeSystem}, 20)
			if err != nil || n != 1 || gate.evaluated != 1 {
				t.Fatalf("projection: n=%d calls=%d error=%v", n, gate.evaluated, err)
			}
			if name == "retry_not_due" {
				if gate.automatic != 0 || gate.activeReads != 0 {
					t.Fatal("input reevaluation bypassed future retry")
				}
			} else if gate.automatic != 1 || gate.window != 30*time.Second {
				t.Fatal("due or fresh-RSS retry was not scheduled once")
			}
			if len(gate.consumed) != 0 || len(gate.rescheduled) != 0 {
				t.Fatal("retry changed observation watermark or deferred a new round")
			}
		})
	}
}

func TestEvaluationSchedulePreservesScopeAndRecoveryGates(t *testing.T) {
	t.Run("other_owner", func(t *testing.T) {
		gate := newEvaluationGate()
		service := scheduler.NewAutoFulfillScheduler(gate, gate, gate, gate)
		n, err := service.RunEvaluationBatch(context.Background(), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString()}, 20)
		if err != nil || n != 0 || gate.evaluated != 0 || gate.automatic != 0 {
			t.Fatal("evaluated a different owner's target", n, err)
		}
	})
	t.Run("changed_projection", func(t *testing.T) {
		gate := newEvaluationGate()
		gate.projectionErr = ports.ErrWantedProjectionChanged
		service := scheduler.NewAutoFulfillScheduler(gate, gate, gate, gate)
		n, err := service.RunEvaluationBatch(context.Background(), &ports.Actor{Type: ports.ActorTypeSystem}, 20)
		if err != nil || n != 0 || gate.automatic != 0 || len(gate.consumed) != 0 {
			t.Fatal("stale projection advanced scheduling", n, err)
		}
	})
	t.Run("active_round", func(t *testing.T) {
		gate := newEvaluationGate()
		gate.fresh, gate.active = true, true
		service := scheduler.NewAutoFulfillScheduler(gate, gate, gate, gate)
		n, err := service.RunEvaluationBatch(context.Background(), &ports.Actor{Type: ports.ActorTypeSystem}, 20)
		if err != nil || n != 1 || gate.automatic != 0 || len(gate.rescheduled) != 1 || gate.rescheduled[0] != 30*time.Second {
			t.Fatal("existing round lost retry protection", n, err)
		}
	})
	t.Run("round_read_failure", func(t *testing.T) {
		gate := newEvaluationGate()
		gate.fresh = true
		gate.activeErr = errors.New("unavailable round snapshot")
		service := scheduler.NewAutoFulfillScheduler(gate, gate, gate, gate)
		n, err := service.RunEvaluationBatch(context.Background(), &ports.Actor{Type: ports.ActorTypeSystem}, 20)
		if !errors.Is(err, gate.activeErr) || n != 1 || gate.automatic != 0 {
			t.Fatal("read failure became absence", n, err)
		}
	})
	t.Run("shared_acquisition", func(t *testing.T) {
		gate := newEvaluationGate()
		gate.projection.WaitReason = "shared_acquisition_available"
		service := scheduler.NewAutoFulfillScheduler(gate, gate, gate, gate)
		n, err := service.RunEvaluationBatch(context.Background(), &ports.Actor{Type: ports.ActorTypeSystem}, 20)
		if err != nil || n != 1 || gate.shared != 1 || gate.automatic != 0 || len(gate.consumed) != 1 || !gate.consumed[0].Equal(gate.now) {
			t.Fatal("shared scheduling changed watermark or created a remote round", n, err)
		}
	})
}
