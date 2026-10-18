package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type UpgradePreviewService struct {
	repo ports.UpgradePreviewRepository
}

func NewUpgradePreviewService(repo ports.UpgradePreviewRepository) *UpgradePreviewService {
	return &UpgradePreviewService{repo}
}
func (s *UpgradePreviewService) Preview(ctx context.Context, a *ports.Actor, r ports.UpgradePreviewRequest) (*ports.UpgradePreview, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if r.SubscriptionID == uuid.Nil || r.CurrentFileID == uuid.Nil || r.CandidateID == uuid.Nil {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	in, e := s.repo.ReadUpgradePreview(ctx, a, r)
	if e != nil {
		return nil, e
	}
	if in == nil {
		return nil, ports.ErrUnavailable
	}
	if r.Expected != nil && *r.Expected != in.Version {
		return nil, ports.ErrEpisodeSourceConflict
	}
	out := &ports.UpgradePreview{Status: "preview_not_executable", CurrentFileID: in.CurrentFileID, CurrentFactID: in.CurrentFactID, CandidateID: in.CandidateID, SourceID: in.SourceID, Version: in.Version, ObservedAt: in.ObservedAt, Reason: in.HoldReason, ExpectedClientDigest: in.ExpectedClientDigest}
	if out.Reason != "" {
		return out, nil
	}
	// Complete original title, release group and allowed attributes precede comparison.
	eligible := media.ExplainMovieCandidate(in.Candidate)
	if eligible.Decision != "accept" {
		out.Reason = "candidate_" + eligible.Reason
		return out, nil
	}
	d := quality.EvaluateUpgrade(in.Candidate.Rules, in.Current, in.Candidate.Profile)
	if in.Mode == ports.UpgradeReplaceIfBetter {
		d = quality.EvaluateUpgradeForMode(in.Candidate.Rules, in.Current, in.Candidate.Profile, in.Mode)
	}
	out.Comparable = d.ScoresKnown
	out.Improvement = d.ShouldUpgrade
	out.Reason = d.Reason
	if d.ScoresKnown {
		out.CurrentScore = &d.CurrentScore
		out.CandidateScore = &d.NewScore
	}
	return out, nil
}
