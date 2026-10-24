package quality

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCustomWordRules = 64
	MaxCustomWordScore = 1000
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

// ValidateCustomWords bounds literal matching and rejects ambiguous declarations.
func ValidateCustomWords(rules []CustomWordRule) error {
	if len(rules) > MaxCustomWordRules {
		return ErrInvalidRules
	}
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		key := strings.ToLower(r.Word)
		if !utf8.ValidString(r.Word) || strings.TrimSpace(r.Word) != r.Word || r.Word == "" || utf8.RuneCountInString(r.Word) > 128 || seen[key] || r.Score < -MaxCustomWordScore || r.Score > MaxCustomWordScore || (r.Required && r.Excluded) || (r.Excluded && r.Score != 0) || (!r.Required && !r.Excluded && r.Score == 0) {
			return ErrInvalidRules
		}
		for _, c := range r.Word {
			if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
				return ErrInvalidRules
			}
		}
		seen[key] = true
	}
	return nil
}

// customWordMatch treats punctuation literally. ASCII word edges exclude
// embedded abbreviations; Chinese terms retain literal substring semantics.
func customWordMatch(title, word string) bool {
	asciiWord := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
	for offset := 0; offset <= len(title)-len(word); {
		at := strings.Index(title[offset:], word)
		if at < 0 {
			return false
		}
		at += offset
		end := at + len(word)
		left := !asciiWord(word[0]) || at == 0 || !asciiWord(title[at-1])
		right := !asciiWord(word[len(word)-1]) || end == len(title) || !asciiWord(title[end])
		if left && right {
			return true
		}
		offset = at + 1
	}
	return false
}

// EvaluateCustomWords grants no media identity or execution permission.
func EvaluateCustomWords(releaseTitle string, rules []CustomWordRule) CustomWordEvaluation {
	if ValidateCustomWords(rules) != nil {
		return CustomWordEvaluation{RejectReason: "自定义关键词规则无效"}
	}
	title := strings.ToLower(releaseTitle)
	matches := make([]bool, len(rules))
	// Exclusions take precedence even when an earlier required word is absent.
	for i, r := range rules {
		matches[i] = customWordMatch(title, strings.ToLower(r.Word))
		if r.Excluded && matches[i] {
			return CustomWordEvaluation{RejectReason: fmt.Sprintf("命中必须排除词: %s", r.Word)}
		}
	}
	for i, r := range rules {
		if r.Required && !matches[i] {
			return CustomWordEvaluation{RejectReason: fmt.Sprintf("缺少必须包含词: %s", r.Word)}
		}
	}
	result := CustomWordEvaluation{Passed: true, MatchedWords: []string{}}
	for i, r := range rules {
		if matches[i] && r.Score != 0 {
			result.BonusScore += r.Score
			result.MatchedWords = append(result.MatchedWords, fmt.Sprintf("%s (%+d)", r.Word, r.Score))
		}
	}
	return result
}
