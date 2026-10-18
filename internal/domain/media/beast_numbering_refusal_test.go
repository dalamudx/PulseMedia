package media_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
)

var beastStandardNumbering = regexp.MustCompile(`(?i)\bS[0-9]{1,2}E[0-9]{1,3}\b|\b[0-9]{1,2}x[0-9]{1,3}\b`)

// Upstream episode numbers without a standard scheme remain original source
// evidence. They cannot establish S01, authorize an endpoint or hide an undercount.
func validateBeastNumberingRefusal(original *corpusEntry, raw json.RawMessage) error {
	if original == nil {
		return fmt.Errorf("missing independent source")
	}
	var source []int
	if b, ok := original.Expected["beast_original_episodes"]; ok {
		if json.Unmarshal(b, &source) != nil || len(source) == 0 {
			return fmt.Errorf("invalid original episode set")
		}
	}
	if b, ok := original.Expected["beast_original_episode"]; ok {
		var n int
		if json.Unmarshal(b, &n) != nil || n < 1 {
			return fmt.Errorf("invalid original episode scalar")
		}
		if len(source) == 0 {
			source = []int{n}
		} else if len(source) != 1 || source[0] != n {
			return fmt.Errorf("original scalar conflicts with full array")
		}
	}
	if len(source) == 0 || beastStandardNumbering.MatchString(original.Name) {
		return nil
	}
	for _, n := range source {
		if n < 1 {
			return fmt.Errorf("invalid independent episode number")
		}
	}
	var w struct {
		Season     int      `json:"season"`
		Episodes   []int    `json:"episodes"`
		Keys       []string `json:"unit_keys"`
		Absolute   int      `json:"absolute_episode"`
		Confidence float64  `json:"confidence"`
		Review     bool     `json:"requires_review"`
		Kind       string   `json:"media_type"`
		Reason     string   `json:"reason"`
	}
	if json.Unmarshal(raw, &w) != nil {
		return fmt.Errorf("malformed complete contract")
	}
	if !w.Review || w.Confidence != 0 || w.Season != 0 || len(w.Episodes) != 0 || len(w.Keys) != 0 {
		return fmt.Errorf("unbound original episodes gained canonical coverage")
	}
	if w.Absolute != 0 && (len(source) != 1 || source[0] != w.Absolute || w.Kind != "tv" || w.Reason != "absolute_mapping_required") {
		return fmt.Errorf("unbound source numbering was truncated or substituted")
	}
	return nil
}

func TestBeastOriginalNumberingNeverDefaultsOrTruncates(t *testing.T) {
	one := &corpusEntry{Name: "[Group] Original - 08 [720p]", Expected: map[string]json.RawMessage{"beast_original_episodes": json.RawMessage(`[8]`)}}
	good := json.RawMessage(`{"season":0,"episodes":[],"unit_keys":[],"absolute_episode":8,"confidence":0,"requires_review":true,"media_type":"tv","reason":"absolute_mapping_required"}`)
	if err := validateBeastNumberingRefusal(one, good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"season":1,"episodes":[8],"unit_keys":["S01E08"],"requires_review":true}`, `{"absolute_episode":9,"confidence":0,"requires_review":true,"media_type":"tv","reason":"absolute_mapping_required"}`, `{"absolute_episode":8,"confidence":0.6,"requires_review":true}`, `{"absolute_episode":8,"confidence":0,"requires_review":true,"media_type":"movie","reason":"absolute_mapping_required"}`} {
		if validateBeastNumberingRefusal(one, json.RawMessage(bad)) == nil {
			t.Fatalf("false scope accepted: %s", bad)
		}
	}
	many := &corpusEntry{Name: one.Name, Expected: map[string]json.RawMessage{"beast_original_episodes": json.RawMessage(`[8,9]`)}}
	if validateBeastNumberingRefusal(many, good) == nil {
		t.Fatal("original full array truncated to one absolute endpoint")
	}
}
