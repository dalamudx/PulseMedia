package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestMovieEpisodeTitleDoesNotEraseTVEvidence(t *testing.T) {
	for _, input := range []string{
		"Saga Episode 2 Homecoming 2001 1080p BluRay x264",
		"Saga.Episode.2.Homecoming.2001.1080p.BluRay.x264",
	} {
		got := media.RecognizeRelease(input)
		if got.MediaType != media.TypeMovie || got.Title != "Saga Episode 2 Homecoming" || got.Year != 2001 || got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Errorf("movie title lost: %+v", got)
		}
	}
	for _, input := range []string{
		"Show Episode 2 2001 1080p WEB-DL",
		"Show Episode 2 Homecoming 1080p WEB-DL",
		"Show 2001 Episode 2 Homecoming 1080p WEB-DL",
		"Episode 2 Homecoming 2001 1080p WEB-DL",
		"Show Episode 2 Homecoming 2001 2002 1080p WEB-DL",
		"Show Episode 2 Homecoming 1080p 2001 WEB-DL",
		"Show Episode 2 Homecoming 2001 E03 1080p WEB-DL",
		"Show Episode 2 Homecoming 2001 season 1 1080p WEB-DL",
		"Show Episode 2 Homecoming 2001 sample 1080p WEB-DL",
	} {
		got := media.RecognizeRelease(input)
		if !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Errorf("uncertain TV/partial evidence became movie permission: %s: %+v", input, got)
		}
	}
	got := media.RecognizeRelease("Show Episode 2 Homecoming 2001 S02E03 1080p WEB-DL")
	if got.MediaType != media.TypeTV || !got.RequiresReview || len(got.UnitKeys) != 0 {
		t.Fatalf("mixed canonical evidence bypassed review: %+v", got)
	}
}
