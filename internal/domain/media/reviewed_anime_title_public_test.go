package media

import (
	"slices"
	"testing"
)

func TestReviewedAnimeTitlePublicGuards(t *testing.T) {
	// A lexical title view must preserve unit guards, evidence and year rules.
	for _, input := range []string{
		"[Hi10] Web Cam German (1998) [DVD 576p]",
		"[Cleo] Web Cam German (1998) [Dual Audio 10bit BD720p][HEVC-x265]",
	} {
		got := RecognizeRelease(input)
		if got.Year != 1998 || got.MediaType != TypeMovie || got.RequiresReview || !slices.Equal(got.UnitKeys, []string{"movie"}) {
			t.Fatalf("year-bearing branch changed: %+v", got)
		}
	}
	standard := RecognizeRelease("[Hi10] Web Cam German S01E01 [DVD 576p]")
	if standard.MediaType != TypeTV || !slices.Equal(standard.UnitKeys, []string{"S01E01"}) {
		t.Fatalf("canonical branch changed: %+v", standard)
	}
	absolute := RecognizeRelease("[Cleo] Web Cam German - 25 [1080p]")
	if absolute.AbsoluteEpisode != 25 || absolute.Title != "Web Cam German" || absolute.Reason != "absolute_mapping_required" || !absolute.RequiresReview || len(absolute.UnitKeys) != 0 {
		t.Fatalf("absolute branch changed: %+v", absolute)
	}
	for _, input := range []string{
		"[Hi10] Ordinary OVA [BDRip-1080p]",
		"[Hi10] Ordinary S01 [DVD 576p]",
		"[Hi10] Ordinary EP1-EP12 [DVD 576p]",
	} {
		got := RecognizeRelease(input)
		if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 {
			t.Fatalf("coverage hold bypassed: %+v", got)
		}
	}
	for _, input := range []string{
		"[Hi10] Ordinary Web Cam German [DVD 576p]",
		"[matheousse] Ordinary Web Cam German MULTi VF/VOSTFR (BD 1080p AAC)",
	} {
		got := RecognizeRelease(input)
		if got.Title != "Ordinary Web Cam German" || got.Year != 0 || !got.RequiresReview || got.Confidence != .6 || got.Reason != "title_only_without_year" || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 || got.RawName != input {
			t.Fatalf("lexical view granted identity or lost evidence: %+v", got)
		}
	}
}
