package media

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"strings"
)

const CandidateDecisionVersion = "movie-candidate-v3"

type CandidateRuleInput struct {
	CandidateID, PolicyID              string
	MediaID, UnitID, RawProfile, Scope string
	MediaVersion, UnitVersion          int64
	PolicyRevision                     int64
	Title, TargetTitle                 string
	Year                               int
	Identities                         map[string]string
	Profile                            quality.Profile
	Rules                              quality.PolicyRules
	SizeBytes                          int64
	HasReference                       bool
	SourceVersion                      int64
}
type CandidateRuleDecision struct {
	Decision        string   `json:"decision"`
	Reason          string   `json:"reason"`
	Score           int      `json:"score"`
	PreferenceScore int      `json:"preference_score,omitempty"`
	MeetsCutoff     bool     `json:"meets_cutoff"`
	Breakdown       []string `json:"breakdown"`
}

func CandidateInputDigest(in CandidateRuleInput) string {
	raw, _ := json.Marshal([]any{CandidateDecisionVersion, EpisodeParserVersion, in})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func SafeCandidateTitle(title string) string {
	if UnsafeCandidateTitle(title) {
		return "资源标题包含敏感引用，已隐藏"
	}
	if len([]rune(title)) > 512 {
		return string([]rune(title)[:512])
	}
	return title
}
func UnsafeCandidateTitle(title string) bool {
	value := strings.ToLower(title)
	for _, s := range []string{"://", "passkey=", "api_key=", "apikey=", "cookie:", "authorization:", "bearer "} {
		if strings.Contains(value, s) {
			return true
		}
	}
	return false
}
func ExplainMovieCandidate(in CandidateRuleInput) CandidateRuleDecision {
	hold := func(reason string) CandidateRuleDecision {
		return CandidateRuleDecision{Decision: "hold", Reason: reason, Breakdown: []string{}}
	}
	if UnsafeCandidateTitle(in.Title) {
		return hold("unsafe_title")
	}
	if in.SourceVersion < 1 {
		return hold("source_unproven")
	}
	if !MatchesAutomaticMovie(in.Title, in.TargetTitle, in.Year, in.Identities) {
		return hold("match_unproven")
	}
	if in.SizeBytes <= 0 {
		return hold("size_unknown")
	}
	if !in.HasReference {
		return hold("reference_missing")
	}
	evaluation := quality.EvaluateCandidate(in.Rules, in.Title, in.Profile)
	if !evaluation.Passed {
		decision := "reject"
		reason := "quality_rejected"
		if evaluation.RejectedReason == "quality_unknown" {
			decision, reason = "hold", "quality_unknown"
		} else if strings.HasPrefix(evaluation.RejectedReason, "命中拒绝词") {
			reason = "reject_term"
		} else if strings.HasPrefix(evaluation.RejectedReason, "命中黑名单") {
			reason = "reject_group"
		}
		return CandidateRuleDecision{Decision: decision, Reason: reason, Breakdown: []string{}}
	}
	return CandidateRuleDecision{Decision: "accept", Reason: "rules_allowed", Score: evaluation.Score, PreferenceScore: evaluation.PreferenceScore, MeetsCutoff: evaluation.MeetsCutoff, Breakdown: evaluation.ScoreBreakdown}
}
