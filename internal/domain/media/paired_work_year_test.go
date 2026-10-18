package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestPairedWorkYearDecorationRetainsTitleAndAmbiguity(t *testing.T) {
	for _, tc := range []struct{ input, title string }{
		{"High Heat *2022* [BRRip.XviD] Lektor PL", "High Heat"},
		{"*2022* High Heat 1080p WEB-DL", "High Heat"},
		{"High * Heat *2022* 1080p WEB-DL", "High * Heat"},
		{"High Heat *2022 1080p WEB-DL", "High Heat *"},
	} {
		r := media.RecognizeRelease(tc.input)
		if r.Title != tc.title || r.Year != 2022 || r.RequiresReview || len(r.UnitKeys) != 1 || r.UnitKeys[0] != "movie" {
			t.Fatal(tc, r)
		}
	}
	for _, raw := range []string{"High Heat *2022* 2023 WEB-DL", "High Heat *2022* S01", "High Heat *2022* S01E01 E02"} {
		r := media.RecognizeRelease(raw)
		if !r.RequiresReview || r.Confidence != 0 || len(r.UnitKeys) != 0 {
			t.Fatal("decorated year bypassed scope/year guards", raw, r)
		}
	}
	for _, tc := range []struct{ input, group string }{
		{"Movie.2022.x264-AFG[EZTVx.to].avi", "AFG"},
		{"Movie.2022.x264-AFG[eztv].avi", "AFG"},
		{"Movie.2022.x264-AFG[unknown-site].avi", "AFG[unknown-site]"},
	} {
		r := media.RecognizeRelease(tc.input)
		if r.RawName != tc.input || r.Quality.Group != tc.group {
			t.Fatal("raw or publisher evidence rewritten", tc, r)
		}
	}
}
