package classification

import (
	"errors"
	"github.com/google/uuid"
	"slices"
	"strings"
	"unicode"
)

var ErrInvalid = errors.New("invalid classification rules")

type Rule struct {
	ID        string    `json:"id"`
	MediaType string    `json:"media_type"`
	Genres    []string  `json:"genres"`
	Languages []string  `json:"languages"`
	Countries []string  `json:"countries"`
	RootID    uuid.UUID `json:"root_id"`
}
type Facts struct {
	MediaType string   `json:"media_type"`
	Genres    []string `json:"genres"`
	Languages []string `json:"languages"`
	Countries []string `json:"countries"`
}
type Match struct {
	RuleID  string    `json:"rule_id"`
	RootID  uuid.UUID `json:"root_id"`
	Reasons []string  `json:"reasons"`
}
type Result struct {
	Status  string     `json:"status"`
	RootID  *uuid.UUID `json:"root_id,omitempty"`
	Matches []Match    `json:"matches"`
	Reasons []string   `json:"reasons"`
}

func Validate(rules []Rule) error {
	if len(rules) > 64 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || len(r.ID) > 64 || strings.TrimSpace(r.ID) != r.ID || strings.ContainsFunc(r.ID, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '-' }) || seen[r.ID] || r.RootID == uuid.Nil || (r.MediaType != "movie" && r.MediaType != "tv") {
			return ErrInvalid
		}
		seen[r.ID] = true
		for _, set := range [][]string{r.Genres, r.Languages, r.Countries} {
			if len(set) > 32 {
				return ErrInvalid
			}
			s := map[string]bool{}
			for _, v := range set {
				if v == "" || len(v) > 64 || strings.TrimSpace(v) != v || strings.ContainsFunc(v, unicode.IsControl) || s[strings.ToLower(v)] {
					return ErrInvalid
				}
				s[strings.ToLower(v)] = true
			}
		}
		for _, v := range r.Languages {
			if len(v) < 2 || len(v) > 3 || strings.IndexFunc(v, func(c rune) bool { return c < 'a' || c > 'z' }) >= 0 {
				return ErrInvalid
			}
		}
		for _, v := range r.Countries {
			if len(v) != 2 || strings.IndexFunc(v, func(c rune) bool { return c < 'A' || c > 'Z' }) >= 0 {
				return ErrInvalid
			}
		}
	}
	return nil
}

// All configured dimensions must match; values within one dimension are alternatives.
// A potentially matching rule with missing facts prevents a definitive selection.
func Evaluate(rules []Rule, f Facts) Result {
	out := Result{Status: "no_match", Matches: []Match{}, Reasons: []string{}}
	if f.MediaType != "movie" && f.MediaType != "tv" {
		out.Status = "insufficient_evidence"
		out.Reasons = append(out.Reasons, "media_type_unknown")
		return out
	}
	unknown := false
	for _, r := range rules {
		if r.MediaType != f.MediaType {
			continue
		}
		match, missing := true, false
		reasons := []string{"media_type"}
		for i, pair := range [][2][]string{{r.Genres, f.Genres}, {r.Languages, f.Languages}, {r.Countries, f.Countries}} {
			if len(pair[0]) == 0 {
				continue
			}
			if pair[1] == nil {
				missing = true
				continue
			}
			ok := false
			for _, want := range pair[0] {
				if slices.ContainsFunc(pair[1], func(v string) bool { return strings.EqualFold(v, want) }) {
					ok = true
					break
				}
			}
			if !ok {
				match = false
			}
			reasons = append(reasons, []string{"genre", "language", "country"}[i])
		}
		if match && missing {
			unknown = true
		}
		if match && !missing {
			out.Matches = append(out.Matches, Match{RuleID: r.ID, RootID: r.RootID, Reasons: reasons})
		}
	}
	roots := map[uuid.UUID]bool{}
	for _, m := range out.Matches {
		roots[m.RootID] = true
	}
	switch {
	case len(roots) > 1:
		out.Status = "conflict"
		out.Reasons = append(out.Reasons, "multiple_target_roots")
	case unknown:
		out.Status = "insufficient_evidence"
		out.Reasons = append(out.Reasons, "trusted_metadata_missing")
	case len(roots) == 1:
		out.Status = "matched"
		for id := range roots {
			v := id
			out.RootID = &v
		}
	default:
		out.Reasons = append(out.Reasons, "no_rule_matched")
	}
	return out
}
