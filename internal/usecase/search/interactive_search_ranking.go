package search

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// RankInteractiveSearchCandidates returns independent rows sorted globally by
// quality score descending, persisted seeders descending, then UUID ascending.
// The persisted JSON decoder enforces parser evidence; titles are never parsed.
// Malformed, oversized (>16 KiB), null, or incomplete/unknown quality profiles
// receive zero score. In particular both current resolution and source evidence
// are required; valid HDR evidence alone cannot promote an unknown profile.
// Old source/resolution parser versions therefore remain unknown for this view.
// Negative policy weights are rejected; negative swarm counts stay below zero
// naturally without subtraction overflow. Stored QualityScore is always ignored.
// No display limit applies here: Read caps only after sorting every candidate.
func RankInteractiveSearchCandidates(rows []ports.InteractiveSearchCandidate, policy quality.Policy) ([]ports.InteractiveSearchCandidate, error) {
	if policy.ID == "" || len(policy.ID) > 128 || !utf8.ValidString(policy.ID) || strings.TrimSpace(policy.ID) != policy.ID || strings.ContainsFunc(policy.ID, unicode.IsControl) || policy.Revision < 1 {
		return nil, ports.ErrRejected
	}
	if err := quality.ValidateRules(quality.PolicyRules{ResolutionOrder: policy.ResolutionOrder, SourceOrder: policy.SourceOrder, PreferHDR: policy.PreferHDR}); err != nil {
		return nil, ports.ErrRejected
	}
	out := make([]ports.InteractiveSearchCandidate, len(rows))
	for i, row := range rows {
		out[i] = cloneInteractiveSearchCandidate(row)
		out[i].QualityScore = 0
		raw := row.Candidate.QualityProfile
		var profile quality.Profile
		if len(raw) <= 16384 && json.Unmarshal(raw, &profile) == nil && quality.KnownProfile(profile) {
			out[i].QualityScore = policy.Score(profile)
		}
	}
	slices.SortStableFunc(out, func(a, b ports.InteractiveSearchCandidate) int {
		if a.QualityScore > b.QualityScore {
			return -1
		}
		if a.QualityScore < b.QualityScore {
			return 1
		}
		if a.Candidate.Seeders > b.Candidate.Seeders {
			return -1
		}
		if a.Candidate.Seeders < b.Candidate.Seeders {
			return 1
		}
		return bytes.Compare(a.Candidate.ID[:], b.Candidate.ID[:])
	})
	return out, nil
}

func cloneInteractiveSearchCandidate(row ports.InteractiveSearchCandidate) ports.InteractiveSearchCandidate {
	row.Candidate.LinkEncrypted = bytes.Clone(row.Candidate.LinkEncrypted)
	row.Candidate.QualityProfile = bytes.Clone(row.Candidate.QualityProfile)
	row.Candidate.InfoHash = cloneInteractiveValue(row.Candidate.InfoHash)
	row.Candidate.PublishDate = cloneInteractiveValue(row.Candidate.PublishDate)
	row.Candidate.MatchedMediaID = cloneInteractiveValue(row.Candidate.MatchedMediaID)
	row.Candidate.MatchedUnitID = cloneInteractiveValue(row.Candidate.MatchedUnitID)
	return row
}
