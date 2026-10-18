package search

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type CandidateExplanationService struct {
	repo ports.CandidateExplanationRepository
}

func NewCandidateExplanationService(repo ports.CandidateExplanationRepository) *CandidateExplanationService {
	return &CandidateExplanationService{repo}
}
func (s *CandidateExplanationService) Read(ctx context.Context, a *ports.Actor, sub, wanted uuid.UUID) (ports.CandidateExplanationReport, error) {
	var out ports.CandidateExplanationReport
	if a == nil {
		return out, ports.ErrUnauthorized
	}
	snapshot, e := s.repo.ReadCandidateExplanation(ctx, a, sub, wanted)
	if e != nil {
		return out, e
	}
	out = ports.CandidateExplanationReport{SubscriptionID: sub, WantedID: wanted, RoundID: snapshot.RoundID, IntentVersion: snapshot.IntentVersion, PolicyRevision: snapshot.PolicyRevision, RoundState: snapshot.RoundState, RoundReason: snapshot.RoundReason, ObservedAt: snapshot.ObservedAt, Items: []ports.CandidateExplanationItem{}, Blockers: snapshot.Status.Blockers, Limited: snapshot.Limited}
	for _, row := range snapshot.Items {
		digest := media.CandidateInputDigest(row.Rule)
		evaluation, e := s.repo.ReadCandidateRuleCache(ctx, digest)
		cached := e == nil && evaluation != nil
		if e != nil && !errors.Is(e, ports.ErrNotFound) {
			return ports.CandidateExplanationReport{}, ports.ErrUnavailable
		}
		if !cached {
			decision := media.ExplainMovieCandidate(row.Rule)
			evaluation = &decision
			if e = s.repo.WriteCandidateRuleCache(ctx, digest, decision); e != nil {
				return ports.CandidateExplanationReport{}, ports.ErrUnavailable
			}
		}
		wait := snapshot.UnitStatus.WaitReason
		available := !row.Blocked && (wait == "missing" || wait == "file_rejected" || wait == "no_qualified_candidate") && evaluation.Decision == "accept" && row.SourceCurrent && len(snapshot.Status.Blockers) == 0 && !snapshot.UnitStatus.IsSatisfied && !snapshot.UnitStatus.ReservationProtected
		if !row.SourceCurrent {
			wait = "source_changed"
		}
		if row.Blocked {
			wait = "release_blocked"
		}
		if available && (wait == "missing" || wait == "no_qualified_candidate" || wait == "file_rejected") {
			wait = ""
		}
		out.Items = append(out.Items, ports.CandidateExplanationItem{ID: row.ID, IndexerID: row.IndexerID, Title: media.SafeCandidateTitle(row.Rule.Title), Decision: evaluation.Decision, Reason: evaluation.Reason, Score: evaluation.Score, MeetsCutoff: evaluation.MeetsCutoff, Breakdown: evaluation.Breakdown, Available: available, Blocked: row.Blocked, WaitReason: wait, InputDigest: digest, Cached: cached})
	}
	return out, nil
}
