package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Synthetic boundary controls are not additional independent gold samples.
func TestDottedInitialTitlesPreserveReleaseBoundaries(t *testing.T) {
	for _, tc := range []struct {
		raw, title string
		year       int
		key        string
	}{
		{"A.I.Artificial.Intelligence.(2001)", "A.I. Artificial Intelligence", 2001, "movie"},
		{"V.H.S.2.2013.720p.BluRay.x264-GRP", "V.H.S. 2", 2013, "movie"},
		{"Movie.Title.2020.1080p.mkv", "Movie Title", 2020, "movie"},
		{"Movie.Title.1080p.mkv", "Movie Title", 0, ""},
		{"A.I.Project.S01E02.1080p.mkv", "A.I. Project", 0, "S01E02"},
	} {
		got := media.RecognizeRelease(tc.raw)
		if got.RawName != tc.raw || got.Title != tc.title || got.Year != tc.year {
			t.Fatalf("title punctuation changed release evidence: %+v", got)
		}
		if tc.key == "" {
			if !got.RequiresReview || len(got.UnitKeys) != 0 {
				t.Fatal("yearless title authorized", got)
			}
			continue
		}
		if got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != tc.key {
			t.Fatal("coverage changed", got)
		}
	}
	for _, raw := range []string{
		"R.I.P.D.1984.2020.1080p.mkv",
		"A.I.Show.S01E02.E03.1080p.mkv",
		"A.I.Show.S01E02 / Film.2020.1080p.mkv",
		"A.I.Show.2024.01.02.1080p.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatal("initials concealed competing year/coverage", got)
		}
	}
}
