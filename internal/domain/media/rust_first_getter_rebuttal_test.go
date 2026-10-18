package media_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// A scalar first getter is only a rebuttal: it cannot assert the full set,
// ordering gaps, canonical identity or a numbering mapping. Null is ignorance.
func validateRustFirstEpisodeRebuttal(labels map[string]json.RawMessage, season int, episodes []int) error {
	for _, key := range []string{"rust_season", "rust_first_episode"} {
		raw, ok := labels[key]
		if !ok || bytes.Equal(raw, []byte("null")) {
			continue
		}
		var n int
		if e := json.Unmarshal(raw, &n); e != nil {
			return fmt.Errorf("nonliteral first getter")
		}
		if key == "rust_season" && n != season {
			return fmt.Errorf("season contradicts scalar getter")
		}
		if key == "rust_first_episode" && !slices.Contains(episodes, n) {
			return fmt.Errorf("missing independently asserted first episode")
		}
	}
	return nil
}

func TestRustFirstGetterRefutesMissingUnitButCannotCertifyFullSet(t *testing.T) {
	labels := map[string]json.RawMessage{"rust_season": json.RawMessage("1"), "rust_first_episode": json.RawMessage("9")}
	if e := validateRustFirstEpisodeRebuttal(labels, 1, []int{9, 10}); e != nil {
		t.Fatal(e)
	}
	if e := validateRustFirstEpisodeRebuttal(labels, 2, []int{9, 10}); e == nil {
		t.Fatal("wrong season allowed")
	}
	if e := validateRustFirstEpisodeRebuttal(labels, 1, []int{10}); e == nil {
		t.Fatal("first getter9 disappeared")
	}
	// The partial getter alone cannot refute missing10. The complete handwritten
	// Expanse9+10 golden is a distinct oracle and DOES reject that undercount.
	if e := validateRustFirstEpisodeRebuttal(labels, 1, []int{9}); e != nil {
		t.Fatal("partial getter falsely claims complete set", e)
	}
	labels["rust_first_episode"] = json.RawMessage("null")
	if e := validateRustFirstEpisodeRebuttal(labels, 1, []int{10}); e != nil {
		t.Fatal("unknown turned into absence", e)
	}
	labels["rust_first_episode"] = json.RawMessage(`{"symbol_ref":"Episode.Nine"}`)
	if e := validateRustFirstEpisodeRebuttal(labels, 1, []int{9}); e == nil {
		t.Fatal("expression granted identity")
	}
}
