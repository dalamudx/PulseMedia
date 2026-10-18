package quality

import (
	"fmt"
	"strings"
)

type CustomWordRule struct {
	Word     string `json:"word"`
	Score    int    `json:"score"`
	Required bool   `json:"required"`
	Excluded bool   `json:"excluded"`
}

type CustomWordEvaluation struct {
	Passed       bool
	BonusScore   int
	RejectReason string
	MatchedWords []string
}

// EvaluateCustomWords evaluates release title against user custom word rules.
func EvaluateCustomWords(releaseTitle string, rules []CustomWordRule) CustomWordEvaluation {
	lowerTitle := strings.ToLower(releaseTitle)
	bonusScore := 0
	matched := make([]string, 0)

	for _, r := range rules {
		wordLower := strings.ToLower(r.Word)
		contains := strings.Contains(lowerTitle, wordLower)

		// 1. Check Excluded rules
		if r.Excluded && contains {
			return CustomWordEvaluation{
				Passed:       false,
				RejectReason: fmt.Sprintf("命中必须排除词: %s", r.Word),
			}
		}

		// 2. Check Required rules
		if r.Required && !contains {
			return CustomWordEvaluation{
				Passed:       false,
				RejectReason: fmt.Sprintf("缺少必须包含词: %s", r.Word),
			}
		}

		// 3. Accumulate bonus score
		if contains && r.Score != 0 {
			bonusScore += r.Score
			matched = append(matched, fmt.Sprintf("%s (%+d)", r.Word, r.Score))
		}
	}

	return CustomWordEvaluation{
		Passed:       true,
		BonusScore:   bonusScore,
		MatchedWords: matched,
	}
}
