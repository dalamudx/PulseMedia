package quality

import (
	"fmt"
)

// UpgradeMode selects the stopping condition; all modes retain known-evidence,
// strictly higher-score and same-tier safeguards.
type UpgradeMode string

const (
	UpgradeDisabled        UpgradeMode = "disabled"
	UpgradeStopAtCutoff    UpgradeMode = "stop_at_cutoff"
	UpgradeReplaceIfBetter UpgradeMode = "replace_if_better"
)

func (m UpgradeMode) Enabled() bool {
	return m == UpgradeStopAtCutoff || m == UpgradeReplaceIfBetter
}

type UpgradeDecision struct {
	// ScoresKnown is false for unknown or incomparable evidence; zero is not a rank.
	ScoresKnown   bool
	ShouldUpgrade bool
	Reason        string
	CurrentScore  int
	NewScore      int
}

// EvaluateUpgrade evaluates whether a new candidate is eligible to replace an existing library file.
// Invariants:
// - If current file already reached CutoffScore, upgrade is rejected.
// - If new candidate score <= current file score, upgrade is rejected.
// - Same tier replacement is disabled by default to prevent endless downloading.
func EvaluateUpgrade(rules PolicyRules, current Profile, candidate Profile) UpgradeDecision {
	return evaluateUpgrade(rules, current, candidate, true)
}

// EvaluateUpgradeForMode uses the frozen subscription mode. Replacement keeps
// comparing improvements after the cutoff; it does not permit same-tier churn.
func EvaluateUpgradeForMode(rules PolicyRules, current Profile, candidate Profile, mode UpgradeMode) UpgradeDecision {
	if !mode.Enabled() {
		return UpgradeDecision{Reason: "upgrade_mode_disabled_or_unknown"}
	}
	return evaluateUpgrade(rules, current, candidate, mode == UpgradeStopAtCutoff)
}

func evaluateUpgrade(rules PolicyRules, current Profile, candidate Profile, stopAtCutoff bool) UpgradeDecision {
	// Unknown evidence is not a low rank. Require the original independent
	// attribute versions before comparing scores; this pure result is not an
	// execution permit and cannot evaluate title reject terms without a title.
	if !KnownProfile(current) || current.ResolutionParserVersion != ResolutionTokenParserVersion {
		return UpgradeDecision{Reason: "current_quality_unknown"}
	}
	if !KnownProfile(candidate) || candidate.ResolutionParserVersion != ResolutionTokenParserVersion {
		return UpgradeDecision{Reason: "candidate_quality_unknown"}
	}
	if ValidateRules(rules) != nil {
		return UpgradeDecision{Reason: "invalid_policy"}
	}
	if _, ok := rules.ResolutionOrder[current.Resolution]; !ok {
		return UpgradeDecision{Reason: "current_policy_incompatible"}
	}
	if _, ok := rules.SourceOrder[current.Source]; !ok {
		return UpgradeDecision{Reason: "current_policy_incompatible"}
	}
	if !EvaluateStoredFile(rules, "", candidate).Passed {
		return UpgradeDecision{Reason: "candidate_policy_rejected"}
	}
	pol := Policy{
		PreferHDR:       rules.PreferHDR,
		ResolutionOrder: rules.ResolutionOrder,
		SourceOrder:     rules.SourceOrder,
	}

	currScore := pol.Score(current)
	newScore := pol.Score(candidate)

	// 1. Cutoff check: if current file already meets or exceeds cutoff score, do not upgrade
	if stopAtCutoff && rules.CutoffScore > 0 && currScore >= rules.CutoffScore {
		return UpgradeDecision{
			ScoresKnown:   true,
			ShouldUpgrade: false,
			Reason:        fmt.Sprintf("current_score_%d_already_reached_cutoff_%d", currScore, rules.CutoffScore),
			CurrentScore:  currScore,
			NewScore:      newScore,
		}
	}

	// 2. Score improvement check
	if newScore <= currScore {
		return UpgradeDecision{
			ScoresKnown:   true,
			ShouldUpgrade: false,
			Reason:        fmt.Sprintf("new_score_%d_not_better_than_current_%d", newScore, currScore),
			CurrentScore:  currScore,
			NewScore:      newScore,
		}
	}

	// 3. Same tier check: if same resolution and same source, do not upgrade
	if current.Resolution != "" && current.Resolution == candidate.Resolution &&
		current.Source != "" && current.Source == candidate.Source {
		return UpgradeDecision{
			ScoresKnown:   true,
			ShouldUpgrade: false,
			Reason:        "same_tier_replacement_disabled",
			CurrentScore:  currScore,
			NewScore:      newScore,
		}
	}

	return UpgradeDecision{
		ScoresKnown:   true,
		ShouldUpgrade: true,
		Reason:        fmt.Sprintf("upgrade_score_%d_to_%d", currScore, newScore),
		CurrentScore:  currScore,
		NewScore:      newScore,
	}
}
