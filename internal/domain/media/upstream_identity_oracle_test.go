package media_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Independently authored upstream years only. Missing fields are not negative assertions.
func TestMITAcceptedYearMatchesIndependentEvidence(t *testing.T) {
	for _, entry := range readMITCorpora(t) {
		r := media.RecognizeRelease(entry.Name)
		if r.RequiresReview {
			continue
		}
		var year int
		if raw, ok := entry.Expected["year"]; ok && json.Unmarshal(raw, &year) == nil && year >= 1900 && year <= 2099 && r.Year != year {
			t.Errorf("accepted year mismatch %q got %d expected %d confidence %.2f", entry.Name, r.Year, year, r.Confidence)
		}
	}
}
