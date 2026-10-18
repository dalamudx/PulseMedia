package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Boundary probes are synthetic and never included in the independent corpus.
func TestSpacedCertifiedSitePreservesWorkAndUnknownPrefixes(t *testing.T) {
	for _, prefix := range []string{"www TamilBlasters tel - ", "www 1TamilBlasters tel - "} {
		raw := prefix + "Film.2020.1080p.WEB-DL.x264-GRP.mkv"
		got := media.RecognizeRelease(raw)
		if got.Title != "Film" || got.Year != 2020 || got.RawName != raw || got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Fatal("known site changed work", got)
		}
		raw = prefix + "Show.S01E01.1080p.WEB-DL.x264-GRP.mkv"
		got = media.RecognizeRelease(raw)
		if got.Title != "Show" || got.RawName != raw || got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "S01E01" {
			t.Fatal("known site hid explicit coverage", got)
		}
		for _, suffix := range []string{"Show.S01E01.S02E01.1080p.mkv", "Show.S01E01.Ep1-43.1080p.mkv", "Show.S01.1080p.mkv", "Bare Work"} {
			raw = prefix + suffix
			got = media.RecognizeRelease(raw)
			if got.RawName != raw || !got.RequiresReview || len(got.UnitKeys) != 0 {
				t.Fatal("site cleanup manufactured authorization", got)
			}
		}
	}
	for _, prefix := range []string{"www Unknown tel - ", "www 2TamilBlasters tel - ", "TamilBlasters tel - ", "www TamilBlasters tell - ", "www TamilBlasters tel ", "Film www TamilBlasters tel - "} {
		raw := prefix + "Work.2020.1080p.mkv"
		got := media.Recognize(raw)
		if got.RawName != raw || got.Title == "Work" {
			t.Fatal("unknown/body prefix discarded", got)
		}
	}
}
