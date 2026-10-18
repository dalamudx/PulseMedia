package quality

import (
	"errors"
	"math"
	"sort"
	"strings"
)

// PackModePreferEpisodes is an explicit preview input, not a published PolicyRules field.
const PackModePreferEpisodes PackMode = "prefer_episodes"

var ErrInvalidTVSelection = errors.New("invalid TV selection inputs")

type EpisodeSelectionCandidate struct {
	ID      string
	Episode int
	Score   int
}

type TVSelection struct {
	Kind         string   // pack, episodes, or hold
	CandidateIDs []string // episode-number order for singles
	Reason       string
	Score        int // weakest selected episode; a pack's score applies to each episode
}

// ValidateTVSelectionPolicy requires an explicit mode and a finite [0,1] ratio.
// Neither an omitted mode nor a threshold is filled from published quality JSON.
func ValidateTVSelectionPolicy(policy PackPolicy) error {
	switch policy.Mode {
	case PackModeNone, PackModePreferPack, PackModeRequirePack, PackModePreferEpisodes:
	default:
		return ErrInvalidTVSelection
	}
	if math.IsNaN(policy.MinMissingRatio) || math.IsInf(policy.MinMissingRatio, 0) || policy.MinMissingRatio < 0 || policy.MinMissingRatio > 1 {
		return ErrInvalidTVSelection
	}
	return nil
}

// ChooseTVSelection compares already eligible candidates. It cannot grant acquisition
// authority. Expected and missing are explicit sets; no title/range inference occurs.
// A singles alternative must cover every missing episode. We compare its weakest
// quality score with the pack score, never sum scores from different-sized plans.
func ChooseTVSelection(policy PackPolicy, expected, missing []int, packs []PackCandidate, singles []EpisodeSelectionCandidate) (TVSelection, error) {
	hold := TVSelection{Kind: "hold", CandidateIDs: []string{}, Reason: "no_eligible_alternative"}
	if ValidateTVSelectionPolicy(policy) != nil || len(expected) == 0 || len(expected) > 1000 {
		return hold, ErrInvalidTVSelection
	}
	full, need := map[int]bool{}, map[int]bool{}
	for _, ep := range expected {
		if ep < 1 || ep > 9999 || full[ep] {
			return hold, ErrInvalidTVSelection
		}
		full[ep] = true
	}
	for _, ep := range missing {
		if !full[ep] || need[ep] {
			return hold, ErrInvalidTVSelection
		}
		need[ep] = true
	}
	if len(need) == 0 {
		hold.Reason = "no_missing_targets"
		return hold, nil
	}
	ids := map[string]bool{}
	var pack *PackCandidate
	for _, p := range packs {
		if p.ID == "" || strings.ContainsRune(p.ID, 0) || ids[p.ID] {
			return hold, ErrInvalidTVSelection
		}
		ids[p.ID] = true
		// Every mode is conservative: this slice only knows complete-season pack review.
		if EvaluatePackCompleteness(PackPolicy{Mode: PackModeRequirePack}, p) != nil || len(p.ExpectedEpisodes) != len(full) {
			continue
		}
		same := true
		for _, ep := range p.ExpectedEpisodes {
			if !full[ep] {
				same = false
			}
		}
		if !same {
			continue
		}
		if pack == nil || p.Score > pack.Score || p.Score == pack.Score && p.ID < pack.ID {
			copy := p
			pack = &copy
		}
	}
	best := map[int]EpisodeSelectionCandidate{}
	for _, c := range singles {
		if c.ID == "" || strings.ContainsRune(c.ID, 0) || ids[c.ID] {
			return hold, ErrInvalidTVSelection
		}
		ids[c.ID] = true
		if !need[c.Episode] {
			continue
		}
		old, ok := best[c.Episode]
		if !ok || c.Score > old.Score || c.Score == old.Score && c.ID < old.ID {
			best[c.Episode] = c
		}
	}
	var episodes *TVSelection
	if len(best) == len(need) {
		keys := append([]int(nil), missing...)
		sort.Ints(keys)
		plan := TVSelection{Kind: "episodes", CandidateIDs: []string{}, Score: best[keys[0]].Score}
		for _, ep := range keys {
			c := best[ep]
			plan.CandidateIDs = append(plan.CandidateIDs, c.ID)
			if c.Score < plan.Score {
				plan.Score = c.Score
			}
		}
		episodes = &plan
	}
	if policy.Mode == PackModeRequirePack {
		if pack == nil {
			hold.Reason = "required_pack_unavailable"
			return hold, nil
		}
		return TVSelection{Kind: "pack", CandidateIDs: []string{pack.ID}, Score: pack.Score, Reason: "required_complete_pack"}, nil
	}
	if pack == nil {
		if episodes != nil {
			episodes.Reason = "only_eligible_alternative"
			return *episodes, nil
		}
		return hold, nil
	}
	chosen := TVSelection{Kind: "pack", CandidateIDs: []string{pack.ID}, Score: pack.Score, Reason: "only_eligible_alternative"}
	if episodes == nil {
		return chosen, nil
	}
	ratio := float64(len(need)) / float64(len(full))
	if policy.Mode == PackModePreferPack && ratio >= policy.MinMissingRatio {
		chosen.Reason = "pack_preference"
		return chosen, nil
	}
	if policy.Mode == PackModePreferEpisodes {
		episodes.Reason = "episode_preference"
		return *episodes, nil
	}
	// none has no format bias; below the pack threshold also uses neutral quality ranking.
	if episodes.Score > chosen.Score || episodes.Score == chosen.Score && selectionKey(episodes.CandidateIDs) < selectionKey(chosen.CandidateIDs) {
		episodes.Reason = "quality_then_stable_id"
		return *episodes, nil
	}
	chosen.Reason = "quality_then_stable_id"
	return chosen, nil
}

func selectionKey(ids []string) string {
	copy := append([]string(nil), ids...)
	sort.Strings(copy)
	return strings.Join(copy, "\x00")
}

