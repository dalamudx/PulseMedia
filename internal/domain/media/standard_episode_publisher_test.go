package media

import "testing"

func TestCertifiedStandardEpisodePublisherPrefixes(t *testing.T) {
	for _, tc := range []struct{ input, title, group string }{
		{"[DKB] Example - S01E03 [1080p][HEVC x265 10bit]", "Example", "DKB"},
		{"[EMBER] Example: Subtitle S01E02 [HEVC WEBRip]", "Example: Subtitle", "EMBER"},
		{"[ToonsHub] Example S01E06 WEB-DL H.264", "Example", "ToonsHub"},
		{"[DKB] Example S01E01 H264-OtherGroup", "Example", ""},
	} {
		got := RecognizeRelease(tc.input)
		if got.Title != tc.title || got.Quality.Group != tc.group || got.RequiresReview || len(got.UnitKeys) != 1 {
			t.Errorf("certified publisher prefix: %+v; title=%q group=%q", got, tc.title, tc.group)
		}
	}
	for _, input := range []string{"[Unlisted] Example S01E01", "[DKBx] Example S01E01", "DKB Example S01E01", "[DKB Example S01E01"} {
		got := RecognizeRelease(input)
		if got.RawName != input || got.Title == "Example" || got.Quality.Group != "" {
			t.Errorf("invented publisher or stripped unknown prefix: %+v", got)
		}
	}
	for _, input := range []string{"[DKB] Example S01E01 S02E03", "[EMBER] Example S01E01-13", "[ToonsHub] Example S01E01 / Other S02E02"} {
		got := RecognizeRelease(input)
		if !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Errorf("publisher bypassed full coverage guard: %+v", got)
		}
	}
}

func TestParentEpisodeEvidenceCannotBeHiddenByBasename(t *testing.T) {
	for _, input := range []string{"Alias S01E01 / Other S02E02", "Alias S01E01 / Movie.2024.{tmdb-123}.mkv", `Alias 1x03\Movie.2024.{tmdb-123}.mkv`, "Alias S01E01 / Other.mkv"} {
		got := RecognizeRelease(input)
		if got.RawName != input || got.Confidence != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 {
			t.Errorf("basename grants conflicting unit authority: %+v", got)
		}
		old := Recognize(input)
		if old.RawName != input || old.Confidence != 0 || old.Identity != nil || !old.RequiresReview {
			t.Errorf("legacy basename grants identity authority: %+v", old)
		}
	}
	for _, input := range []string{"Library/Example.S01E01.mkv", "Example/Season 01/Example.S01E01.mkv", "Library.2024/Example.S01E01.mkv"} {
		got := RecognizeRelease(input)
		if got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "S01E01" {
			t.Errorf("ordinary directory changed canonical evidence: %+v", got)
		}
	}
}
