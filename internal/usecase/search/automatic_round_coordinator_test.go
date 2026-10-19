package search_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

type routedTVRounds struct {
	start func(context.Context, ports.AutomaticTVRoundStart) (*ports.AutomaticTVRound, error)
}

func (r routedTVRounds) StartAutomaticTVRound(ctx context.Context, in ports.AutomaticTVRoundStart) (*ports.AutomaticTVRound, error) {
	return r.start(ctx, in)
}
func (r routedTVRounds) LoadAutomaticTVSource(context.Context, ports.AutomaticTVSourceTask) (*ports.AutomaticTVSourceWork, error) {
	panic("source execution is outside scheduling")
}
func (r routedTVRounds) FinishAutomaticTVSource(context.Context, ports.AutomaticTVSourceWork, ports.AutomaticTVSourceOutcome) error {
	panic("source execution is outside scheduling")
}
func (r routedTVRounds) RecoverAutomaticTVSources(context.Context, int32) (int, error) {
	panic("source recovery is outside scheduling")
}

type routedMovieRounds struct {
	automatic func(context.Context, uuid.UUID, time.Duration) error
	shared    func(context.Context, uuid.UUID) error
	finalize  func(context.Context, uuid.UUID, ports.RoundJobAuthority) error
}

func (r routedMovieRounds) ScheduleAutomaticRound(ctx context.Context, id uuid.UUID, window time.Duration) error {
	return r.automatic(ctx, id, window)
}
func (r routedMovieRounds) ScheduleSharedRound(ctx context.Context, id uuid.UUID) error {
	return r.shared(ctx, id)
}
func (r routedMovieRounds) FinalizeScheduledRound(ctx context.Context, id uuid.UUID, lease ports.RoundJobAuthority) error {
	return r.finalize(ctx, id, lease)
}

func TestAutomaticRoundCoordinatorOnlyFallsBackForActualNonTV(t *testing.T) {
	for _, reason := range []error{nil, ports.ErrUnavailable, ports.ErrForbidden, ports.ErrAcquisitionRoundAborted, context.Canceled, ports.ErrAutomaticTVNotApplicable, fmt.Errorf("target: %w", ports.ErrAutomaticTVNotApplicable)} {
		t.Run(fmt.Sprint(reason), func(t *testing.T) {
			wanted := uuid.New()
			window := 30 * time.Second
			calls := 0
			movieFailure := errors.New("movie scheduling result")
			movie := routedMovieRounds{automatic: func(_ context.Context, id uuid.UUID, duration time.Duration) error {
				calls++
				if id != wanted || duration != window {
					t.Fatal("movie fallback changed the target")
				}
				return movieFailure
			}}
			tv := routedTVRounds{start: func(_ context.Context, in ports.AutomaticTVRoundStart) (*ports.AutomaticTVRound, error) {
				if in.WantedID != wanted || in.Window != window {
					t.Fatal("TV scheduling changed the target")
				}
				return &ports.AutomaticTVRound{ID: uuid.New()}, reason
			}}
			err := search.NewAutomaticRoundCoordinator(movie, tv).ScheduleAutomaticRound(context.Background(), wanted, window)
			if errors.Is(reason, ports.ErrAutomaticTVNotApplicable) {
				if calls != 1 || !errors.Is(err, movieFailure) {
					t.Fatalf("non-TV fallback calls=%d error=%v", calls, err)
				}
			} else if calls != 0 || !errors.Is(err, reason) {
				t.Fatalf("TV outcome must remain original: calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestAutomaticRoundCoordinatorPreservesLegacyAuthority(t *testing.T) {
	wanted, round, job := uuid.New(), uuid.New(), uuid.New()
	authority := ports.RoundJobAuthority{ID: job, Owner: "movie-owner", Token: 7}
	shared, finalized := 0, 0
	movie := routedMovieRounds{
		shared: func(_ context.Context, id uuid.UUID) error {
			if id != wanted {
				t.Fatal("changed shared target")
			}
			shared++
			return nil
		},
		finalize: func(_ context.Context, id uuid.UUID, got ports.RoundJobAuthority) error {
			if id != round || got != authority {
				t.Fatal("changed finalize authority")
			}
			finalized++
			return nil
		},
	}
	tv := routedTVRounds{start: func(context.Context, ports.AutomaticTVRoundStart) (*ports.AutomaticTVRound, error) {
		t.Fatal("legacy job used TV authority")
		return nil, nil
	}}
	coordinator := search.NewAutomaticRoundCoordinator(movie, tv)
	if err := coordinator.ScheduleSharedRound(context.Background(), wanted); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.FinalizeScheduledRound(context.Background(), round, authority); err != nil {
		t.Fatal(err)
	}
	if shared != 1 || finalized != 1 {
		t.Fatalf("shared=%d finalized=%d", shared, finalized)
	}
}

func TestAutomaticRoundCoordinatorMissingDependencies(t *testing.T) {
	ctx, id := context.Background(), uuid.New()
	coordinator := search.NewAutomaticRoundCoordinator(nil, nil)
	if !errors.Is(coordinator.ScheduleAutomaticRound(ctx, id, time.Second), ports.ErrUnavailable) || !errors.Is(coordinator.ScheduleSharedRound(ctx, id), ports.ErrUnavailable) || !errors.Is(coordinator.FinalizeScheduledRound(ctx, id, ports.RoundJobAuthority{}), ports.ErrUnavailable) {
		t.Fatal("missing dependencies granted scheduling")
	}
	tv := routedTVRounds{start: func(context.Context, ports.AutomaticTVRoundStart) (*ports.AutomaticTVRound, error) {
		return nil, ports.ErrAutomaticTVNotApplicable
	}}
	if err := search.NewAutomaticRoundCoordinator(nil, tv).ScheduleAutomaticRound(ctx, id, time.Second); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("missing movie fallback: %v", err)
	}
}
