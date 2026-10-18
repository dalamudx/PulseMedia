package search

import (
	"context"
	"encoding/json"
	"errors"
	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"time"
)

var ErrRoundAborted = ports.ErrAcquisitionRoundAborted
var ErrRoundAlreadyClosed = errors.New("search round is already closed")

type FinalizeReport = ports.RoundDecision

type RoundCoordinator struct {
	repo      ports.SearchRoundRepository
	clock     ports.Clock
	finalizer ports.MovieRoundFinalizer
}

func NewRoundCoordinator(repo ports.SearchRoundRepository, clock ports.Clock, finalizer ports.MovieRoundFinalizer) *RoundCoordinator {
	return &RoundCoordinator{repo, clock, finalizer}
}
func (c *RoundCoordinator) StartRound(ctx context.Context, wanted uuid.UUID, window time.Duration) (*ports.SearchRound, error) {
	return c.repo.StartRound(ctx, ports.SearchRoundStart{WantedID: wanted, Window: window, ManualNow: c.clock.Now})
}
func (c *RoundCoordinator) StartScheduledRound(ctx context.Context, wanted uuid.UUID, window time.Duration) (*ports.SearchRound, error) {
	return c.repo.StartRound(ctx, ports.SearchRoundStart{WantedID: wanted, Window: window, Scheduled: true})
}
func (c *RoundCoordinator) StartSharedRound(ctx context.Context, wanted uuid.UUID) (*ports.SearchRound, error) {
	return c.repo.StartRound(ctx, ports.SearchRoundStart{WantedID: wanted, Window: 30 * time.Second, Scheduled: true, Shared: true, QualifyShared: qualifySharedSource})
}
func (c *RoundCoordinator) AddCandidate(ctx context.Context, round, candidate uuid.UUID) error {
	return c.repo.AddCandidate(ctx, round, candidate)
}
func (c *RoundCoordinator) FinalizeRound(ctx context.Context, actor *ports.Actor, id uuid.UUID) (*FinalizeReport, error) {
	return c.finalizer.FinalizeMovieRound(ctx, actor, id, nil)
}
func (c *RoundCoordinator) FinalizeLeasedRound(ctx context.Context, id uuid.UUID, authority ports.RoundJobAuthority) (*FinalizeReport, error) {
	return c.finalizer.FinalizeMovieRound(ctx, &ports.Actor{Type: ports.ActorTypeSystem, ID: id.String(), Name: "subscription scheduler"}, id, &authority)
}
func (c *RoundCoordinator) ScheduleAutomaticRound(ctx context.Context, wanted uuid.UUID, window time.Duration) error {
	_, err := c.StartScheduledRound(ctx, wanted, window)
	return err
}
func (c *RoundCoordinator) ScheduleSharedRound(ctx context.Context, wanted uuid.UUID) error {
	_, err := c.StartSharedRound(ctx, wanted)
	return err
}
func (c *RoundCoordinator) FinalizeScheduledRound(ctx context.Context, id uuid.UUID, authority ports.RoundJobAuthority) error {
	_, err := c.FinalizeLeasedRound(ctx, id, authority)
	return err
}

func qualifySharedSource(input ports.SearchSharedSource) bool {
	source := input.Candidate
	if source.SizeBytes <= 0 || len(source.LinkEncrypted) == 0 || !domainMedia.MatchesAutomaticMovie(source.Title, input.Title, int(input.Year), input.Identities) {
		return false
	}
	var rules quality.PolicyRules
	var profile quality.Profile
	return json.Unmarshal(input.Rules, &rules) == nil && json.Unmarshal(source.QualityProfile, &profile) == nil && quality.EvaluateCandidate(rules, source.Title, profile).Passed
}
