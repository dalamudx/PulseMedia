package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestUnsupportedSeasonWidthCannotBecomeMovie(t *testing.T) {
	for _, input := range []string{
		"Formula1.S2025E86.Italy.Grand.Prix.1080i.HDTV.MPA2.0.H.264-playTV",
		"Show.S2025.2020.1080p.WEB-DL.x264-Group",
		"Show.S123456E01.2020.1080p.WEB-DL.x264-Group",
		"Show.S01E01.S2025E86.1080p.WEB-DL.x264-Group",
		"Show.1x01.S2025E86.1080p.WEB-DL.x264-Group",
	} {
		got := media.RecognizeRelease(input)
		if got.RawName != input || got.MediaType != media.TypeTV || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 {
			t.Fatalf("unsupported season became movie or partial coverage: %+v", got)
		}
	}
	for _, input := range []string{"Show.S01E01.1080p.WEB-DL.x264-Group", "Show.1x01.1080p.WEB-DL.x264-Group"} {
		got := media.RecognizeRelease(input)
		if got.RequiresReview || got.Season != 1 || len(got.Episodes) != 1 || got.Episodes[0] != 1 {
			t.Fatalf("supported season rejected: %+v", got)
		}
	}
	got := media.RecognizeRelease("2012.AC3.720p.BluRay.x264-XXL")
	if got.Title != "2012" || got.Year != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 {
		t.Fatalf("numeric movie title changed: %+v", got)
	}
}
