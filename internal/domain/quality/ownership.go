package quality

// OwnershipContractVersion binds cached projections to the quality evidence rules.
// Advancing it requires an append-only migration of wanted_projection_validity.
const OwnershipContractVersion = "ownership.quality.v3"

type FileEvidence struct {
	Title    string
	Profile  Profile
	Verified bool
}
type Ownership struct {
	Owned, Unknown, Rejected bool
	Score                    int
}

// AssessOwnership evaluates every current verified import under one immutable
// allowed-rule revision. Unknown evidence is never rank zero or an owned file.
// Acquire ownership does not require the upgrade cutoff.
func AssessOwnership(rules PolicyRules, files []FileEvidence) Ownership {
	result := Ownership{}
	for _, file := range files {
		if !file.Verified || !KnownProfile(file.Profile) {
			result.Unknown = true
			continue
		}
		evaluation := EvaluateCandidate(rules, file.Title, file.Profile)
		if !evaluation.Passed {
			result.Rejected = true
			continue
		}
		if !result.Owned || evaluation.Score > result.Score {
			result.Score = evaluation.Score
		}
		result.Owned = true
	}
	return result
}
