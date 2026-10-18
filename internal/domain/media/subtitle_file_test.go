package media

import "testing"

func TestSubtitleInputsPreserveRawAndRefuseUnitAuthority(t *testing.T) {
	for _, input := range []string{"Yurusarezaru_mono2.srt", "Movie.2024.1080p.srt", "Movie.2024.{tmdb-123}.mkv.SRT", "Show.S01E03.720p.ass", "[HorribleSubs] Show - 03 [720p].ssa", "Movie.2024.vtt", "Movie.2024.sub", "Movie.2024.idx"} {
		got := RecognizeRelease(input)
		if got.RawName != input || got.Confidence != 0 || !got.RequiresReview || got.Reason != "non_video_file" || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.Season != 0 || got.AbsoluteEpisode != 0 {
			t.Errorf("subtitle grants unit authority: %+v", got)
		}
		legacy := Recognize(input)
		if legacy.RawName != input || legacy.Confidence != 0 || !legacy.RequiresReview || legacy.Identity != nil || MatchesAutomaticMovie(input, "Movie", 2024, map[string]string{"tmdb.movie": "123"}) {
			t.Errorf("legacy subtitle grants identity authority: %+v", legacy)
		}
	}
	if got := RecognizeRelease("Yurusarezaru_mono2.srt"); got.Title != "Yurusarezaru mono2" {
		t.Fatal("original subtitle title lost", got)
	}
	for _, input := range []string{"SRT.Movie.2024.mkv", "Movie.2024.srtx", "Movie.2024.mkv"} {
		if got := RecognizeRelease(input); got.Reason == "non_video_file" {
			t.Fatalf("ordinary filename refused: %+v", got)
		}
	}
}

func TestZIPArchivesNeverAuthorizeVideoUnits(t *testing.T) {
	for _, input := range []string{"Movie.2024.1080p.WEB-DL.zip", "Movie.2024.{tmdb-123}.mkv.ZIP", "Show.S01E03.720p.zip", "[HorribleSubs] Show - 03 [720p].zip", "Show.S01E01.1x02.zip"} {
		t.Run(input, func(t *testing.T) {
			r, m := RecognizeRelease(input), Recognize(input)
			if r.RawName != input || r.Confidence != 0 || !r.RequiresReview || r.Reason != "non_video_file" || len(r.UnitKeys) != 0 || len(r.Episodes) != 0 || r.AbsoluteEpisode != 0 {
				t.Fatalf("archive gained release authority: %+v", r)
			}
			if m.RawName != input || m.Identity != nil || m.Confidence != 0 || !m.RequiresReview || MatchesAutomaticMovie(input, "Movie", 2024, map[string]string{"tmdb.movie": "123"}) {
				t.Fatalf("archive gained movie authority: %+v", m)
			}
		})
	}
	for _, input := range []string{"ZIP.Movie.2024.mkv", "Movie.2024.zipx", "Movie.2024.mkv"} {
		if got := RecognizeRelease(input); got.Reason == "non_video_file" {
			t.Fatalf("ordinary title refused: %+v", got)
		}
	}
}
