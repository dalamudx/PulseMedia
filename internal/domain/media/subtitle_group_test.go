package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestSubtitleMetadataCannotCertifyReleaseGroup(t *testing.T) {
	for _, suffix := range []string{"Vostfr", "VOSTFR", "vostfr"} {
		input := "Movie.2024.1080p.BluRay.x264-" + suffix + ".mkv"
		got := media.RecognizeRelease(input)
		if got.RawName != input || got.Title != "Movie" || got.Year != 2024 || got.Quality.Group != "" || media.Recognize(input).Quality.Group != "" {
			t.Errorf("subtitle metadata became publication identity: %+v", got)
		}
	}
	for _, group := range []string{"VOSTFRx", "VOSTFR2", "OTHER"} {
		input := "Movie.2024.1080p.BluRay.x264-" + group + ".mkv"
		if got := media.RecognizeRelease(input); got.Quality.Group != group {
			t.Errorf("unknown group substring was erased: %+v", got)
		}
	}
	input := "Vostfr.2024.1080p.BluRay.x264-GROUP.mkv"
	if got := media.RecognizeRelease(input); got.Title != "Vostfr" || got.Year != 2024 || got.Quality.Group != "GROUP" {
		t.Errorf("title word changed: %+v", got)
	}
}
