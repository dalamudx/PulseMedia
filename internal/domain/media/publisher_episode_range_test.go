package media

import "testing"

func TestPublisherEpisodeRangesNeverAuthorizeEndpoints(t *testing.T) {
	for _, input := range []string{
		"[Erai-raws] Made in Abyss - 01~13 [1080p AVC-YUV444P10][E-AC3][Multiple Subtitle]",
		"[KaiDubs] One Piece - 575-587 [720p] [English Dub] [CC] [AMZN-DL]",
		"[AC] Fairy Tail 278-328",
		"[NoobSubs] Berserk 2017 13-24 (1080p 8bit AAC MP4)",
		"[Group] Series - 01-12 [720p] / Movie (2020) {tmdb-1}.mkv",
		"[Group] Series - 01-12 [720p] / Series.S01E01.1080p.mkv",
		"[Group] Series - 01-12 [720p] / [Other] Series - 03 [720p].mkv",
		"[Group] Series - 01-12 S01E01 [720p]",
	} {
		t.Run(input, func(t *testing.T) {
			got := RecognizeRelease(input)
			if got.RawName != input || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.Season != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
				t.Fatalf("publisher range retained partial identity/coverage: %+v", got)
			}
			legacy := Recognize(input)
			if !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Identity != nil {
				t.Fatalf("legacy movie identity bypassed original range: %+v", legacy)
			}
		})
	}
}

func TestPublisherRangeDetectionPreservesMovieYearIntervals(t *testing.T) {
	input := "[Studio] Documentary - 1984-2020 [1080p].mkv"
	got := RecognizeRelease(input)
	if publisherEpisodeRangeHint(input) != nil || !got.RequiresReview || len(got.UnitKeys) != 0 {
		t.Fatalf("movie-year interval changed existing review: %+v", got)
	}
	for _, input := range []string{"Film (2020) 1080p WEB-DL x264-GRP.mkv", "[Group] Series - 03 [720p].mkv"} {
		got := RecognizeRelease(input)
		if input[0] == '[' {
			if !got.IsAnime || got.AbsoluteEpisode != 3 || got.Reason != "absolute_mapping_required" {
				t.Fatalf("single absolute changed: %+v", got)
			}
		} else if got.RequiresReview || got.Year != 2020 || len(got.UnitKeys) != 1 {
			t.Fatalf("normal movie changed: %+v", got)
		}
	}
}
