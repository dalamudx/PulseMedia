package media_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var rakunEpisodeRangeLiteral = regexp.MustCompile(`^[0-9]{1,4}-[0-9]{1,4}$`)

func validRakunEpisodeRange(value string) bool {
	if !rakunEpisodeRangeLiteral.MatchString(value) {
		return false
	}
	bounds := strings.Split(value, "-")
	first, _ := strconv.Atoi(bounds[0])
	last, _ := strconv.Atoi(bounds[1])
	return first > 0 && last >= first
}

// Keep the full original interval string, including leading zeroes. Neither
// endpoint is a canonical episode claim, and intervals are never expanded.
func validateRakunLiteralRangeRefusal(original *corpusEntry, expected json.RawMessage) error {
	raw, exists := original.Expected["rakun_episode_range"]
	if !exists {
		return nil
	}
	var claim string
	var fields map[string]string
	if json.Unmarshal(raw, &claim) != nil || !validRakunEpisodeRange(claim) || json.Unmarshal(original.Expected["original_rakun_fields"], &fields) != nil || fields["episode"] != claim {
		return fmt.Errorf("original literal episode interval was truncated or rewritten")
	}
	var w struct {
		Type       string   `json:"media_type"`
		Review     bool     `json:"requires_review"`
		Confidence float64  `json:"confidence"`
		Season     int      `json:"season"`
		Absolute   int      `json:"absolute_episode"`
		Anime      bool     `json:"is_anime"`
		Episodes   []int    `json:"episodes"`
		Keys       []string `json:"unit_keys"`
		Reason     string   `json:"reason"`
	}
	if err := json.Unmarshal(expected, &w); err != nil {
		return err
	}
	if w.Type != "tv" || !w.Review || w.Confidence != 0 || w.Season != 0 || w.Absolute != 0 || w.Anime || len(w.Episodes) != 0 || len(w.Keys) != 0 || (w.Reason != "episode_coverage_unresolved" && w.Reason != "conflicting_episode_coverage") {
		return fmt.Errorf("literal interval requires zero authority without endpoint or inferred coverage")
	}
	return nil
}

func TestRakunLiteralEpisodeRangesCannotAuthorizeEndpoints(t *testing.T) {
	original, err := mappedRakunLiteralLabels(map[string]string{"filename": "Input", "name": "Series", "episode": "001-013"})
	if err != nil {
		t.Fatal(err)
	}
	if string(original.Expected["rakun_episode_range"]) != `"001-013"` || original.Expected["episode"] != nil {
		t.Fatal("literal interval lost original form or became a scalar")
	}
	base := map[string]any{"media_type": "tv", "requires_review": true, "confidence": 0, "reason": "episode_coverage_unresolved"}
	for _, c := range []struct {
		field string
		value any
	}{
		{"confidence", .95}, {"requires_review", false}, {"season", 1}, {"absolute_episode", 1}, {"is_anime", true}, {"episodes", []int{1}}, {"episodes", []int{13}}, {"episodes", []int{1, 13}}, {"unit_keys", []string{"S01E01"}}, {"media_type", "movie"}, {"reason", "absolute_mapping_required"},
	} {
		mutant := map[string]any{}
		for k, v := range base {
			mutant[k] = v
		}
		mutant[c.field] = c.value
		raw, _ := json.Marshal(mutant)
		if validateRakunLiteralRangeRefusal(original, raw) == nil {
			t.Fatalf("partial range admitted: %s", raw)
		}
	}
	raw, _ := json.Marshal(base)
	if err := validateRakunLiteralRangeRefusal(original, raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"0-13", "13-1", "1~13", "1-13-26", "1-13+14", "1-13 ", "1-10000", "17.5"} {
		if validRakunEpisodeRange(bad) {
			t.Fatal("nonliteral/invalid interval admitted", bad)
		}
	}
	original.Expected["rakun_episode_range"] = json.RawMessage(`"1-13"`)
	if validateRakunLiteralRangeRefusal(original, raw) == nil {
		t.Fatal("original interval spelling rewritten")
	}
}
