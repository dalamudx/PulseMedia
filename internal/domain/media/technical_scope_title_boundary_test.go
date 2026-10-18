package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

// Mutation controls do not contribute independent golden inputs.
func TestTechnicalScopeWordsBeforeWorkYearRemainTitles(t *testing.T) {
	for _, tc := range []struct{ raw, title string }{
		{"SUBPACK.2024.1080p.BluRay.x264-GRP", "SUBPACK"},
		{"Movie.SUBPACK.2024.1080p.BluRay.x264-GRP", "Movie SUBPACK"},
		{"Digital.Extras.2024.1080p.BluRay.x264-GRP", "Digital Extras"},
		{"Extras.2024.1080p.BluRay.x264-GRP", "Extras"},
	} {
		t.Run(tc.title, func(t *testing.T) {
			got, old := media.RecognizeRelease(tc.raw), media.Recognize(tc.raw)
			if got.Title != tc.title || got.Year != 2024 || got.RequiresReview || got.Confidence != .9 || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" || got.RawName != tc.raw {
				t.Fatalf("ordinary title interpreted as release scope: %+v", got)
			}
			if old.Title != tc.title || old.Year != 2024 || old.RequiresReview || old.Confidence != .9 || old.RawName != tc.raw {
				t.Fatalf("ordinary title interpreted as release scope in legacy entry: %+v", old)
			}
		})
	}
}
