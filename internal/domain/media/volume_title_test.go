package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"strings"
	"testing"
)

// Synthetic boundary controls are excluded from independently sourced counts.
func TestVolumeTitlePunctuationCannotChangeCoverage(t *testing.T) {
	for _, tc := range []struct {
		raw, title string
		year       int
	}{
		{"Guardians of the Galaxy Vol. 3 (2023) 1080p.mkv", "Guardians of the Galaxy Vol. 3", 2023},
		{"Work.Vol.3.2023.1080p.mkv", "Work Vol 3", 2023},
		{"Show.Vol.3.S01E02.1080p.mkv", "Show Vol 3", 0},
	} {
		got := media.RecognizeRelease(tc.raw)
		if got.Title != tc.title || got.Year != tc.year || got.RequiresReview || len(got.UnitKeys) != 1 {
			t.Fatalf("volume spelling changed identity/coverage: %+v", got)
		}
		if got.MediaType == media.TypeTV && got.UnitKeys[0] != "S01E02" {
			t.Fatalf("volume added episode: %+v", got)
		}
	}
	for _, raw := range []string{"Work.Vol. 2024.1080p.mkv", "Work.Revol. 3 (2023) 1080p.mkv"} {
		got := media.RecognizeRelease(raw)
		if strings.Contains(got.Title, "Vol.") || strings.Contains(got.Title, "Revol.") {
			t.Fatalf("year or embedded word treated as volume abbreviation: %+v", got)
		}
	}
	for _, raw := range []string{"Show.S01E01.Vol. 3.S01E02.1080p.mkv", "Show.Vol. 3.2024.01.02.1080p.mkv"} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatalf("volume punctuation hid uncertain episode/date: %+v", got)
		}
	}
}
