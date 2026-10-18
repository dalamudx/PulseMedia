package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestBDRemuxCompleteMarkerAndYearlessTitle(t *testing.T) {
	for _, raw := range []string{"Deadpool [BDRemux 1080p]", "Deadpool [bdremux 1080p]", "Deadpool BDREMUX 1080p BluRay"} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || got.Title != "Deadpool" || got.Year != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || got.Quality.Source != quality.SourceRemux || got.Quality.SourceParserVersion != quality.SourceTokenParserVersion {
			t.Fatalf("complete remux marker lost or title/authorization changed: %+v", got)
		}
	}
	for _, raw := range []string{"Film.2020.1080p.MyBDRemux.mkv", "Film.2020.1080p.BDRemuxish.mkv", "Film.2020.1080p.BDRemux.WEB-DL.mkv", "Film.2020.1080p.BDRemux.DVDRip.mkv"} {
		got := media.RecognizeRelease(raw)
		if got.Quality.Source != "" || got.Quality.SourceParserVersion != "" {
			t.Fatalf("substring or competing source became a fact: %+v", got)
		}
	}
	got := media.RecognizeRelease("Film BDRemux 2020 1080p")
	if got.Year != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 {
		t.Fatalf("technical-tail year became identity: %+v", got)
	}
}
