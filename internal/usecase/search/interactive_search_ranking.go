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

// RankInteractiveSearchCandidates sorts all persisted observations before any
// display cap. Title words are preferences, never inferred quality facts.
// Unknown profiles retain zero for legacy display. With explicit word rules,
// unknown evidence cannot outrank known evidence solely through a penalty.
// Stored scores are ignored; every score belongs to this frozen policy.
func RankInteractiveSearchCandidates(rows []ports.InteractiveSearchCandidate, policy quality.Policy) ([]ports.InteractiveSearchCandidate, error) {
	if policy.ID == "" || len(policy.ID) > 128 || !utf8.ValidString(policy.ID) || strings.TrimSpace(policy.ID) != policy.ID || strings.ContainsFunc(policy.ID, unicode.IsControl) || policy.Revision < 1 {
		return nil, ports.ErrRejected
	}
	if err := quality.ValidateRules(quality.PolicyRules{ResolutionOrder: policy.ResolutionOrder, SourceOrder: policy.SourceOrder, PreferHDR: policy.PreferHDR, CustomWords: policy.CustomWords}); err != nil {
		return nil, ports.ErrRejected
	}
	type ranked struct {
		row   ports.InteractiveSearchCandidate
		known bool
	}
	rankedRows := make([]ranked, 0, len(rows))
	for _, row := range rows {
		words := quality.EvaluateCustomWords(row.Candidate.Title, policy.CustomWords)
		if !words.Passed {
			continue
		}
		entry := ranked{row: cloneInteractiveSearchCandidate(row)}
		entry.row.QualityScore, entry.row.PreferenceScore = 0, 0
		raw := row.Candidate.QualityProfile
		var profile quality.Profile
		entry.known = len(raw) <= 16384 && json.Unmarshal(raw, &profile) == nil && quality.KnownProfile(profile)
		if entry.known {
			entry.row.QualityScore = policy.Score(profile)
			entry.row.PreferenceScore = words.BonusScore
		}
		rankedRows = append(rankedRows, entry)
	}
	slices.SortStableFunc(rankedRows, func(a, b ranked) int {
		if len(policy.CustomWords) != 0 && a.known != b.known {
			if a.known {
				return -1
			}
			return 1
		}
		left, right := a.row.QualityScore+a.row.PreferenceScore, b.row.QualityScore+b.row.PreferenceScore
		if left > right {
			return -1
		}
		if left < right {
			return 1
		}
		if a.row.Candidate.Seeders > b.row.Candidate.Seeders {
			return -1
		}
		if a.row.Candidate.Seeders < b.row.Candidate.Seeders {
			return 1
		}
		return bytes.Compare(a.row.Candidate.ID[:], b.row.Candidate.ID[:])
	})
	out := make([]ports.InteractiveSearchCandidate, len(rankedRows))
	for i := range rankedRows {
		out[i] = rankedRows[i].row
	}
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
