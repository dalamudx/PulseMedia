package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestSeriesYearIsSeparateWithoutGuessingNumericTitles(t *testing.T) {
	for _, tc := range []struct {
		input, title string
		year         int
	}{
		{"The.Flash.2014.S01E04.1080p.HDTV.x264", "The Flash", 2014},
		{"Doctor.Who.(2005).8x11.720p.HDTV.x264", "Doctor Who", 2005},
		{"1923.S01E01.1080p.WEB-DL.x264", "1923", 0},
		{"1923.1x01.1080p.WEB-DL.x264", "1923", 0},
		{"The.100.S01E01.1080p.WEB-DL.x264", "The 100", 0},
	} {
		t.Run(tc.input, func(t *testing.T) {
			p := media.RecognizeRelease(tc.input)
			if p.Title != tc.title || p.Year != tc.year || p.RequiresReview || len(p.UnitKeys) != 1 {
				t.Fatalf("year/title evidence conflated: %+v", p)
			}
		})
	}
	for _, input := range []string{"1923.2023.S01E01.1080p.WEB-DL.x264", "Show.2014.2015.1x01.1080p.WEB-DL.x264"} {
		p := media.RecognizeRelease(input)
		if !p.RequiresReview || p.Confidence != 0 || len(p.UnitKeys) != 0 || p.Reason != "ambiguous_media_year" {
			t.Fatal("conflicting dates became identity", p)
		}
	}
}
