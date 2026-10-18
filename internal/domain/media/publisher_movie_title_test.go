package media

import "testing"

func TestUnboundOVATechnicalHintNeverCertifiesUnit(t *testing.T) {
	for _, input := range []string{
		"[FansubAnimesForever] Highschool of the Dead OVA [BDRip-1080p-8bit].mkv",
		"Film OVA [BDRip-1080p] {tmdb-1}.mkv",
		"Film OVA [BDRip-1080p] / Series.S01E01.mkv",
	} {
		got := RecognizeRelease(input)
		if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 {
			t.Fatalf("OVA metadata granted identity or partial coverage: %+v", got)
		}
		legacy := Recognize(input)
		if !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Identity != nil {
			t.Fatalf("legacy OVA bypass: %+v", legacy)
		}
	}
	for _, input := range []string{"The OVA Story (2020) 1080p.mkv", "Film Coffret (2020) 1080p.mkv"} {
		got := RecognizeRelease(input)
		if got.RequiresReview || got.Year != 2020 || len(got.UnitKeys) != 1 {
			t.Fatalf("ordinary title changed: %+v", got)
		}
	}
}

func TestCertifiedPublisherMovieTitlePreservesUnknownEvidence(t *testing.T) {
	for _, input := range []string{
		"[UnknownPublisher] Film [1080p]",
		"[Dragonier] Film [1080p][UnknownTitle]",
		"[Erai-raws] Film [UnknownQuality]",
		"Film [1080p][86FFE17E]",
	} {
		if got := stripCertifiedPublisherMovieTitle(input); got != input {
			t.Fatalf("unknown evidence erased: %q => %q", input, got)
		}
	}
}
