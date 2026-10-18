package subscription

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	statusdomain "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

func (s *EpisodeMappingService) ExplainCandidate(ctx context.Context, a *ports.Actor, id, candidateID uuid.UUID) (*MappedReleaseExplanation, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	v, e := s.repo.ReadEpisodeCandidate(ctx, a, id, candidateID)
	if e != nil {
		return nil, e
	}
	out := &MappedReleaseExplanation{SubscriptionID: id, MediaID: v.Mapping.MediaID, MappingRevision: v.Mapping.MappingRevision, IntentVersion: v.Mapping.IntentVersion, ParserVersion: media.EpisodeParserVersion, InputStamp: v.Mapping.InputStamp, SourceHash: v.Mapping.SourceHash, ObservedAt: v.Mapping.ObservedAt, ValidUntil: v.Mapping.ValidUntil, Rows: []ports.EpisodeGroupMappingRow{}}
	if media.UnsafeCandidateTitle(v.Name) || strings.ContainsAny(v.Name, "/\\") || len(v.Name) > 512 {
		out.Reason = "unsafe_title"
	} else if !v.Current {
		out.Reason = v.HoldReason
	} else if v.Mapping.Mode == "standard" && v.Mapping.Current {
		if media.HasConflictingTVIdentity(v.Name, v.Mapping.TMDBID) {
			out.Reason = "media_identity_conflict"
		} else {
			units := make([]media.NumberingUnit, 0, len(v.CanonicalUnits))
			for _, u := range v.CanonicalUnits {
				units = append(units, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Key: u.Key, Version: u.Version, Season: u.Season, Episode: u.Episode, Retired: u.Retired})
			}
			resolved, reason := media.ResolveStandardCoverage(media.RecognizeRelease(v.Name), units)
			out.Reason = reason
			for _, r := range resolved {
				out.Rows = append(out.Rows, ports.EpisodeGroupMappingRow{SourceNumber: r.SourceNumber, EpisodeID: r.EpisodeID, UnitID: r.UnitID, UnitKey: r.UnitKey, UnitVersion: r.UnitVersion})
			}
			out.CoverageKnown = len(out.Rows) > 0
		}
	} else {
		out, e = explainMappedReleaseState(id, &v.Mapping, v.Name)
		if e != nil {
			return nil, e
		}
	}
	out.IdentityDecision, out.IdentityReason = media.SuggestTVIdentity(v.Name, v.ParentTitle, v.ParentOriginalTitle, v.ParentYear, v.Mapping.TMDBID)
	out.QualityDecision = "hold"
	out.QualityReason = "policy_unavailable"
	if v.Target.PolicyAvailable {
		evaluated := quality.EvaluateCandidate(v.Target.Rules, v.Name, v.Quality)
		out.QualityReason = "quality_rejected"
		if evaluated.Passed {
			out.QualityDecision = "allow"
			out.QualityReason = "quality_allowed"
		} else if evaluated.RejectedReason == "quality_unknown" {
			out.QualityDecision = "hold"
			out.QualityReason = "quality_unknown"
		} else {
			out.QualityDecision = "reject"
		}
	}
	out.TargetReason = "coverage_unresolved"
	if out.CoverageKnown && len(out.Rows) > 1 {
		out.TargetReason = "multi_unit_review_required"
	}
	if out.CoverageKnown && len(out.Rows) == 1 {
		out.TargetReason = "target_not_in_scope"
		report := statusdomain.DeriveProjection(v.Target)
		for _, item := range report.Items {
			if item.UnitID != out.Rows[0].UnitID {
				continue
			}
			if item.IsSatisfied {
				out.TargetReason = "already_owned"
			} else if item.WaitReason != "" {
				out.TargetReason = item.WaitReason
			} else {
				out.TargetReason = "target_missing"
			}
			break
		}
	}

	out.CandidateID = &v.CandidateID
	out.CandidateInputStamp = v.InputStamp
	out.SourceCurrent = &v.Current
	return out, nil
}
