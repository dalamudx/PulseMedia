package media_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// These immutable declarations can refute accepted coverage. They do not
// create canonical units, measured attributes, or execution authority.
func retainVideoLiteralCoverage(labels, fields map[string]json.RawMessage) error {
	for key, retained := range map[string]string{"seasons": "video_seasons", "episodeNumbers": "video_episodes", "isTv": "video_is_tv"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		if key == "isTv" {
			if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
				return fmt.Errorf("nonliteral video type")
			}
		} else {
			var numbers []int
			if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) || json.Unmarshal(raw, &numbers) != nil {
				return fmt.Errorf("nonliteral video scope")
			}
			seen := map[int]bool{}
			for _, n := range numbers {
				if n < 0 || (key == "episodeNumbers" && n == 0) || seen[n] {
					return fmt.Errorf("invalid video scope")
				}
				seen[n] = true
			}
		}
		if prior, ok := labels[retained]; ok && !bytes.Equal(prior, raw) {
			return fmt.Errorf("conflicting original video coverage")
		}
		labels[retained] = append(json.RawMessage(nil), raw...)
	}
	return nil
}

func validateVideoCoverageRebuttals(labels map[string]json.RawMessage, kind string, season int, episodes []int, keys []string, review bool) error {
	if review && len(keys) > 0 {
		return fmt.Errorf("review cannot certify video units")
	}
	if len(keys) == 0 {
		return nil
	}
	if raw, ok := labels["video_is_tv"]; ok && ((kind == "movie" && bytes.Equal(raw, []byte("true"))) || (kind == "tv" && bytes.Equal(raw, []byte("false")))) {
		return fmt.Errorf("video type conflicts")
	}
	for key, want := range map[string][]int{"video_seasons": {season}, "video_episodes": episodes} {
		raw, ok := labels[key]
		if !ok {
			continue
		}
		var original []int
		if err := json.Unmarshal(raw, &original); err != nil {
			return err
		}
		if kind == "movie" {
			if len(original) > 0 {
				return fmt.Errorf("movie cannot hide declared video coverage")
			}
			continue
		}
		a, b := slices.Clone(original), slices.Clone(want)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			return fmt.Errorf("partial or conflicting video coverage")
		}
	}
	return nil
}

func TestVideoLiteralCoverageRebuttalRejectsPartialPromotion(t *testing.T) {
	fields := map[string]json.RawMessage{"seasons": json.RawMessage(`[2]`), "episodeNumbers": json.RawMessage(`[3,5]`), "isTv": json.RawMessage(`true`)}
	before, _ := json.Marshal(fields)
	labels := map[string]json.RawMessage{}
	if err := retainVideoLiteralCoverage(labels, fields); err != nil {
		t.Fatal(err)
	}
	if err := validateVideoCoverageRebuttals(labels, "tv", 2, []int{5, 3}, []string{"S02E03", "S02E05"}, false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind string
		season     int
		episodes   []int
		keys       []string
		review     bool
	}{
		{"first-only", "tv", 2, []int{3}, []string{"S02E03"}, false},
		{"invented-extra", "tv", 2, []int{3, 4, 5}, []string{"S02E03", "S02E04", "S02E05"}, false},
		{"wrong-season", "tv", 1, []int{3, 5}, []string{"S01E03", "S01E05"}, false},
		{"movie-fallback", "movie", 0, nil, []string{"movie"}, false},
		{"review-promoted", "tv", 2, []int{3, 5}, []string{"S02E03", "S02E05"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if validateVideoCoverageRebuttals(labels, tc.kind, tc.season, tc.episodes, tc.keys, tc.review) == nil {
				t.Fatal("scope declaration bypassed")
			}
		})
	}
	if err := validateVideoCoverageRebuttals(labels, "tv", 0, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(fields)
	if !bytes.Equal(before, after) {
		t.Fatal("original labels changed")
	}
	for _, raw := range []string{`null`, `1`, `[1.5]`, `[-1]`, `[1,1]`, `{"symbol_ref":"Season.TWO"}`} {
		if retainVideoLiteralCoverage(map[string]json.RawMessage{}, map[string]json.RawMessage{"seasons": json.RawMessage(raw)}) == nil {
			t.Fatal("nonliteral/invalid scope accepted", raw)
		}
	}
	if retainVideoLiteralCoverage(map[string]json.RawMessage{}, map[string]json.RawMessage{"episodeNumbers": json.RawMessage(`[0]`)}) == nil {
		t.Fatal("zero episode accepted")
	}
	if retainVideoLiteralCoverage(map[string]json.RawMessage{}, map[string]json.RawMessage{"isTv": json.RawMessage(`null`)}) == nil {
		t.Fatal("null type accepted")
	}
	labels["video_seasons"] = json.RawMessage(`[1,2]`)
	if validateVideoCoverageRebuttals(labels, "tv", 2, []int{3, 5}, []string{"S02E03", "S02E05"}, false) == nil {
		t.Fatal("partial season set accepted")
	}
}
