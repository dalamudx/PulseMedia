package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestNumericTechnicalGroupCannotSupplyWorkYear(t *testing.T) {
	for _, raw := range []string{"Appaloosa.1080p.Bluray.x264-1920.mkv", "Appaloosa.1080p.Bluray.x264-1920[ettv].mkv"} {
		r := media.RecognizeRelease(raw)
		if r.Year != 0 || r.Title != "Appaloosa" || !r.RequiresReview || len(r.UnitKeys) != 0 || r.Confidence != .6 {
			t.Fatal("numeric group certified movie", raw, r)
		}
		if media.MatchesAutomaticMovie(raw, "Appaloosa", 1920, nil) {
			t.Fatal("numeric group authorized automatic match", raw)
		}
	}
	for _, raw := range []string{"Appaloosa.2020.1080p.Bluray.x264-1920.mkv", "Appaloosa-2020.mkv"} {
		r := media.RecognizeRelease(raw)
		if r.Year != 2020 || r.Title != "Appaloosa" || r.RequiresReview {
			t.Fatal("explicit work year lost", raw, r)
		}
	}
	r := media.RecognizeRelease("Appaloosa.1080p.Bluray.x264-1920 {tmdb-550}.mkv")
	if r.Year != 0 {
		t.Fatal("ID reintroduced numeric group year", r)
	}
	r = media.RecognizeRelease("Appaloosa.1984.2020.1080p.Bluray.x264-1920.mkv")
	if r.Reason != "ambiguous_media_year" || r.Year != 0 || len(r.UnitKeys) != 0 {
		t.Fatal("group suppressed distinct work years", r)
	}
}

func TestTechnicalDashDoesNotDeclareReleaseGroup(t *testing.T) {
	for _, suffix := range []string{"WEB-DL", "Blu-Ray", "DTS-HD", "WEB-DL[ettv]"} {
		r := media.RecognizeRelease("Fixture.2020.1080p." + suffix + ".mkv")
		if r.Quality.Group != "" {
			t.Fatal("technical marker became group", suffix, r.Quality)
		}
	}
	r := media.RecognizeRelease("Fixture.2020.1080p.WEB-DL-DL.mkv")
	if r.Quality.Group != "DL" {
		t.Fatal("explicit group after technical marker lost", r.Quality)
	}
}
