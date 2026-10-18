package quality

import (
	"fmt"
	"strings"
)

// PolicyRules represents configurable matching and ranking rules.
type PolicyRules struct {
	CutoffScore     int                `json:"cutoff_score"`
	PreferHDR       bool               `json:"prefer_hdr"`
	ResolutionOrder map[Resolution]int `json:"resolution_order"`
	SourceOrder     map[Source]int     `json:"source_order"`
	RejectTerms     []string           `json:"reject_terms"`
	RejectGroups    []string           `json:"reject_groups"`
}

type EvaluationPreview struct {
	Passed         bool     `json:"passed"`
	RejectedReason string   `json:"rejected_reason,omitempty"`
	Score          int      `json:"score"`
	MeetsCutoff    bool     `json:"meets_cutoff"`
	ScoreBreakdown []string `json:"score_breakdown"`
}

// EvaluateCandidate tests a candidate against policy rules, explaining pass/reject reasons and score breakdown.
func EvaluateCandidate(rules PolicyRules, releaseTitle string, prof Profile) EvaluationPreview {
	if !KnownProfile(prof) {
		return EvaluationPreview{RejectedReason: "quality_unknown", ScoreBreakdown: []string{}}
	}
	if _, ok := rules.ResolutionOrder[prof.Resolution]; !ok {
		return EvaluationPreview{RejectedReason: "分辨率不在允许列表", ScoreBreakdown: []string{}}
	}
	if _, ok := rules.SourceOrder[prof.Source]; !ok {
		return EvaluationPreview{RejectedReason: "来源不在允许列表", ScoreBreakdown: []string{}}
	}
	lowerTitle := strings.ToLower(releaseTitle)

	// 1. Check RejectTerms in title
	for _, term := range rules.RejectTerms {
		termLower := strings.ToLower(term)
		if termLower != "" && strings.Contains(lowerTitle, termLower) {
			return EvaluationPreview{
				Passed:         false,
				RejectedReason: fmt.Sprintf("命中拒绝词: %s", term),
			}
		}
	}

	// 2. Check RejectGroups
	for _, group := range rules.RejectGroups {
		if RejectedReleaseGroup(prof.Group, group) || rejectedRawReleaseGroup(releaseTitle, group) {
			return EvaluationPreview{
				Passed:         false,
				RejectedReason: fmt.Sprintf("命中黑名单发布组: %s", group),
			}
		}
	}

	// 3. Compute score with breakdown
	score := 0
	breakdown := make([]string, 0, 4)

	if resScore, ok := rules.ResolutionOrder[prof.Resolution]; ok {
		score += resScore
		breakdown = append(breakdown, fmt.Sprintf("+%d (分辨率 %s)", resScore, prof.Resolution))
	}

	if srcScore, ok := rules.SourceOrder[prof.Source]; ok && prof.SourceParserVersion == SourceTokenParserVersion {
		score += srcScore
		breakdown = append(breakdown, fmt.Sprintf("+%d (来源 %s)", srcScore, prof.Source))
	}

	if rules.PreferHDR && VerifiedHDR(prof) && (prof.HDR == HDRDolbyVision || prof.HDR == HDR10Plus || prof.HDR == HDR10) {
		score += 15
		breakdown = append(breakdown, fmt.Sprintf("+15 (HDR %s)", prof.HDR))
	}

	meetsCutoff := score >= rules.CutoffScore

	return EvaluationPreview{
		Passed:         true,
		Score:          score,
		MeetsCutoff:    meetsCutoff,
		ScoreBreakdown: breakdown,
	}
}
