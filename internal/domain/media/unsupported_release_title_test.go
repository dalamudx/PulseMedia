package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

// Synthetic boundary controls do not increase the independent source count.
func TestUnsupportedTitleTailKeepsWordsAndUnknownFacts(t *testing.T) {
	for _, tc := range []struct{ raw, title string }{
		{"The.Fighter.DVDR-MPTDVD", "The Fighter"},
		{"Sunshine.Cleaning.DVDR-Replica", "Sunshine Cleaning"},
		{"Inglorious.Basterds.CAM.XviD-CAMELOT", "Inglorious Basterds"},
		{"Inglourious.Basterds.SCR.XViD-xSCR", "Inglourious Basterds"},
		{"Body.Cam.1080p.x264-GROUP", "Body Cam"},
		{"Body.CAM.XviD-GROUP", "Body CAM XviD-GROUP"},
		{"Interrogation.Cam.XviD-GROUP", "Interrogation Cam XviD-GROUP"},
		{"Movie.Title.DVDRish-MPTDVD", "Movie Title DVDRish-MPTDVD"},
		{"Movie.Title.SCR.Script", "Movie Title SCR Script"},
		{"Movie.Title.CAM", "Movie Title CAM"},
	} {
		got := media.RecognizeRelease(tc.raw)
		if got.RawName != tc.raw || got.Title != tc.title || !got.RequiresReview || len(got.UnitKeys) != 0 || got.Quality.Source != "" {
			t.Fatalf("unsupported tail changed title/raw or authorized facts: %+v", got)
		}
	}
	for _, raw := range []string{
		"Movie.Title.S01E02.E03.CAM.XviD-GROUP",
		"Movie.Title.S01E02 / Film.Title.CAM.XviD-GROUP",
		"Movie.Title.2024.01.02.SCR.XviD-GROUP",
		"Movie.Title.1984.2009.DVDR-GROUP",
	} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatal("tail concealed competing identity or coverage", got)
		}
	}
	for _, tc := range []struct{ raw, title string }{
		{"Body.Cam.2020.1080p.BluRay.x264-GROUP", "Body Cam"},
		{"The.SCR.2009.1080p.BluRay.x264-GROUP", "The SCR"},
	} {
		got := media.RecognizeRelease(tc.raw)
		if got.Title != tc.title || got.RequiresReview || got.RawName != tc.raw {
			t.Fatal("release cleanup erased year-bound title word", got)
		}
	}
}
