package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Boundary controls are synthetic and do not increase the independent count.
func TestParenthesizedNumericTitleKeepsYearBoundary(t *testing.T) {
	for _, tc := range []struct {
		raw, title string
		year       int
	}{
		{"(500).Days.Of.Summer.(2009).DTS.1080p.BluRay.x264.NLsubs", "(500) Days Of Summer", 2009},
		{"(500).Days.Of.Summer.1080p.mkv", "(500) Days Of Summer", 0},
		{"Movie.Title.(2009).1080p.mkv", "Movie Title", 2009},
		{"(2009).Movie.Title.1080p.mkv", "Movie Title", 2009},
		{"Ordinary.Movie.2009.1080p.mkv", "Ordinary Movie", 2009},
	} {
		got := media.RecognizeRelease(tc.raw)
		if got.RawName != tc.raw || got.Title != tc.title || got.Year != tc.year {
			t.Fatalf("lost balanced numeric title/year evidence: %+v", got)
		}
		if tc.year == 0 && (!got.RequiresReview || len(got.UnitKeys) != 0) {
			t.Fatal("yearless numeric title authorized", got)
		}
	}
	for _, raw := range []string{
		"(500).Days.Of.Summer.1984.2009.1080p.mkv",
		"(500).Show.S01E02.E03.1080p.mkv",
		"(500).Show.S01E02 / Movie.2009.1080p.mkv",
		"(500).Show.2024.01.02.1080p.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatal("parentheses concealed competing identity/coverage", got)
		}
	}
}
