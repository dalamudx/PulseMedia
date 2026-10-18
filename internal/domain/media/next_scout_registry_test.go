package media_test

import (
	"encoding/json"
	"fmt"
)

// These are fixed, individually asserted MIT inputs, never a source-wide grant.
func isNextScoutSource(source string) bool {
	switch source {
	case "walker-media-name-parser", "walker-media-name-parser-extra", "machard-releascenify", "ngtrio-tnp", "spann-tvshowfile-date", "matsimitsu-release-parser":
		return true
	default:
		return false
	}
}

func originalNextScoutReviewLabels(source, input string, expected json.RawMessage) (*corpusEntry, error) {
	if !isNextScoutSource(source) {
		return nil, fmt.Errorf("unreviewed next-source origin: %s", source)
	}
	original, err := originalNextScoutAReviewLabels(source, input)
	if err == nil {
		return original, validateNextScoutAOriginalRebuttals(original, expected)
	}
	// The second reader owns the disjoint fixed [63,125) partition. Both
	// readers pin the originals and refuse inputs outside their partition.
	original, err = originalNextScoutBReviewLabels(source, input)
	if err != nil {
		return nil, fmt.Errorf("next-source original not proven: %w", err)
	}
	return original, validateNextScoutBSourceContract(original, expected)
}
