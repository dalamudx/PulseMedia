package quality_test

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestCustomWordsLiteralBoundaries(t *testing.T) {
	cases := []struct {
		name, title, word string
		match             bool
	}{
		{"exact", "DV", "dv", true},
		{"delimiters", "Movie.2020.1080p_DV-Atmos", "DV", true},
		{"embedded_suffix", "Movie.DVD", "DV", false},
		{"embedded_prefix", "Movie.SCAM", "CAM", false},
		{"embedded_both", "Movie.Atmosphere", "Atmos", false},
		{"later_valid", "DVD.Movie.DV", "DV", true},
		{"numeric_boundary", "Movie.x2640", "x264", false},
		{"literal_phrase", "Movie.Dolby Vision", "dolby vision", true},
		{"literal_punctuation", "Movie.WEB-DL", "WEB-DL", true},
		{"no_regex", "Movie.1080p", ".*", false},
		{"chinese", "电影国语中字", "国语", true},
		{"mixed", "电影DV中字", "DV", true},
		{"short_title", "D", "Dolby Vision", false},
		{"empty_title", "", "DV", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quality.EvaluateCustomWords(tc.title, []quality.CustomWordRule{{Word: tc.word, Score: 1}})
			if !got.Passed || (got.BonusScore == 1) != tc.match {
				t.Fatalf("literal match: %+v", got)
			}
		})
	}
}

func TestCustomWordsRejectAmbiguousRules(t *testing.T) {
	cases := []struct {
		name  string
		rules []quality.CustomWordRule
	}{
		{"empty", []quality.CustomWordRule{{Word: "", Score: 1}}},
		{"leading_space", []quality.CustomWordRule{{Word: " DV", Score: 1}}},
		{"trailing_space", []quality.CustomWordRule{{Word: "DV ", Score: 1}}},
		{"duplicate_case", []quality.CustomWordRule{{Word: "DV", Score: 1}, {Word: "dv", Required: true}}},
		{"contradiction", []quality.CustomWordRule{{Word: "DV", Required: true, Excluded: true}}},
		{"excluded_bonus", []quality.CustomWordRule{{Word: "CAM", Excluded: true, Score: 1}}},
		{"noop", []quality.CustomWordRule{{Word: "DV"}}},
		{"overflow", []quality.CustomWordRule{{Word: "DV", Score: math.MaxInt}}},
		{"underflow", []quality.CustomWordRule{{Word: "DV", Score: math.MinInt}}},
		{"newline", []quality.CustomWordRule{{Word: "D\nV", Score: 1}}},
		{"tab", []quality.CustomWordRule{{Word: "D\tV", Score: 1}}},
		{"invisible", []quality.CustomWordRule{{Word: "D\u200bV", Score: 1}}},
		{"invalid_utf8", []quality.CustomWordRule{{Word: string([]byte{255}), Score: 1}}},
		{"too_long", []quality.CustomWordRule{{Word: strings.Repeat("字", 129), Score: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if quality.ValidateCustomWords(tc.rules) == nil {
				t.Fatal("invalid declaration accepted")
			}
			got := quality.EvaluateCustomWords("DV.CAM", tc.rules)
			if got.Passed || got.BonusScore != 0 || len(got.MatchedWords) != 0 {
				t.Fatalf("invalid declaration grants score: %+v", got)
			}
		})
	}
}

func TestCustomWordsExcludeBeforeRequired(t *testing.T) {
	rules := []quality.CustomWordRule{{Word: "2160p", Required: true}, {Word: "CAM", Excluded: true}, {Word: "DV", Score: 1000}}
	got := quality.EvaluateCustomWords("Movie.1080p.CAM.DV", rules)
	if got.Passed || got.RejectReason != "命中必须排除词: CAM" || got.BonusScore != 0 || len(got.MatchedWords) != 0 {
		t.Fatalf("exclusion not final: %+v", got)
	}
	got = quality.EvaluateCustomWords("Movie.1080p.DV", rules)
	if got.Passed || got.RejectReason != "缺少必须包含词: 2160p" || got.BonusScore != 0 {
		t.Fatalf("required gate bypassed: %+v", got)
	}
}

func TestCustomWordsCountEachRuleOnceAndKeepInput(t *testing.T) {
	rules := []quality.CustomWordRule{{Word: "DV", Score: 200, Required: true}, {Word: "Atmos", Score: 100}, {Word: "Dubbed", Score: -50}}
	before := append([]quality.CustomWordRule(nil), rules...)
	got := quality.EvaluateCustomWords("DV.DV.Atmos.Atmos.Dubbed", rules)
	if !got.Passed || got.BonusScore != 250 || len(got.MatchedWords) != 3 || !reflect.DeepEqual(before, rules) {
		t.Fatalf("repeat or mutation: %+v", got)
	}
	got.MatchedWords[0] = "changed"
	next := quality.EvaluateCustomWords("DV.Atmos.Dubbed", rules)
	if next.MatchedWords[0] != "DV (+200)" {
		t.Fatal("result aliases later evaluation")
	}
}

func TestCustomWordsFiniteScoreBounds(t *testing.T) {
	for _, sign := range []int{-1, 1} {
		t.Run(fmt.Sprint(sign), func(t *testing.T) {
			rules := make([]quality.CustomWordRule, quality.MaxCustomWordRules)
			words := make([]string, len(rules))
			for i := range rules {
				words[i] = fmt.Sprintf("tag%d", i)
				rules[i] = quality.CustomWordRule{Word: words[i], Score: sign * quality.MaxCustomWordScore}
			}
			got := quality.EvaluateCustomWords(strings.Join(words, "."), rules)
			if !got.Passed || got.BonusScore != sign*64000 || len(got.MatchedWords) != 64 {
				t.Fatalf("finite sum: %+v", got)
			}
			rules = append(rules, quality.CustomWordRule{Word: "extra", Score: 1})
			if quality.ValidateCustomWords(rules) == nil {
				t.Fatal("unbounded rule list")
			}
		})
	}
}

func TestCustomWordsEmptyConfigurationCompatible(t *testing.T) {
	for _, rules := range [][]quality.CustomWordRule{nil, {}} {
		got := quality.EvaluateCustomWords("Movie.DVD.SCAM", rules)
		if !got.Passed || got.BonusScore != 0 || len(got.MatchedWords) != 0 {
			t.Fatalf("empty policy changed: %+v", got)
		}
	}
}
