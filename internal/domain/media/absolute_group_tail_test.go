package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestAbsoluteEpisodeTailDoesNotNamePublisher(t *testing.T) {
	for _, input := range []string{
		"[EMBER] Sousou no Frieren - 01.mkv",
		"[EMBER] Sousou no Frieren - 01v3.mkv",
		"[Publisher] Show - 1234v12.mkv",
	} {
		got := media.RecognizeRelease(input)
		if got.RawName != input || got.Quality.Group != "" || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 {
			t.Fatalf("absolute episode tail acquired publisher or unit authority: %+v", got)
		}
	}
	for _, input := range []string{"Movie.2020.1080p.WEB-DL.x264-01", "Movie.2020.1080p.WEB-DL.x264-1234"} {
		got := media.RecognizeRelease(input)
		want := "01"
		if input[len(input)-4:] == "1234" {
			want = "1234"
		}
		if got.Quality.Group != want {
			t.Fatalf("unrelated numeric publisher erased: %+v", got)
		}
	}
	got := media.RecognizeRelease("[MTBB] Mushoku Tensei - 05v3 [30C35CEC].mkv")
	if got.Quality.Group != "MTBB" || got.AbsoluteEpisode != 5 || !got.RequiresReview || len(got.UnitKeys) != 0 {
		t.Fatalf("bound publisher/absolute evidence changed: %+v", got)
	}
}
