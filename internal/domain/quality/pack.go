package quality

import (
	"errors"
	"fmt"
)

var (
	ErrIncompletePack = errors.New("incomplete season pack rejected under require_pack policy")
)

type PackMode string

const (
	PackModeNone        PackMode = "none"
	PackModePreferPack  PackMode = "prefer_pack"
	PackModeRequirePack PackMode = "require_pack"
)

type PackPolicy struct {
	Mode            PackMode
	MinMissingRatio float64 // e.g. 0.5 (50% missing episodes to trigger pack preference)
}

type PackCandidate struct {
	ID              string
	Title           string
	Season          int
	StartEp         int
	EndEp           int
	TotalSeasonEps  int
	Score           int
	HasUnknownFiles bool
	// Full current season and explicitly observed coverage, never inferred from endpoints.
	ExpectedEpisodes    []int
	CoveredEpisodes     []int
	SeasonMetadataKnown bool
	CoverageKnown       bool
}

// IsCompleteSeason checks exact explicit sets. Range endpoints and a COMPLETE title are summaries, not proof.
func (p PackCandidate) IsCompleteSeason() bool {
	if !p.SeasonMetadataKnown || !p.CoverageKnown || p.HasUnknownFiles || p.Season < 1 || p.TotalSeasonEps < 1 || p.TotalSeasonEps > 1000 || len(p.ExpectedEpisodes) != p.TotalSeasonEps || len(p.CoveredEpisodes) != len(p.ExpectedEpisodes) {
		return false
	}
	expected := map[int]bool{}
	for _, ep := range p.ExpectedEpisodes {
		if ep < 1 || ep > 9999 || expected[ep] {
			return false
		}
		expected[ep] = true
	}
	seen := map[int]bool{}
	for _, ep := range p.CoveredEpisodes {
		if !expected[ep] || seen[ep] {
			return false
		}
		seen[ep] = true
	}
	return true
}

// EvaluatePackCompleteness validates a season pack against policy constraints.
func EvaluatePackCompleteness(policy PackPolicy, pack PackCandidate) error {
	if policy.Mode == PackModeRequirePack {
		if !pack.IsCompleteSeason() {
			return fmt.Errorf("%w: pack covers E%02d-E%02d but season has %d episodes",
				ErrIncompletePack, pMax(pack.StartEp, 1), pack.EndEp, pack.TotalSeasonEps)
		}
	}
	return nil
}

func pMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
