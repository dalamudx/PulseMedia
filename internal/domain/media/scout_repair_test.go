package media

import (
	"reflect"
	"testing"
)

func TestScoutRepairRawEvidenceAtBothAPIs(t *testing.T) {
	for _, tc := range []struct {
		raw, title string
		collection bool
	}{
		{"arranged.xvid-reserved.avi", "arranged", false},
		{"Unseen Orchard.xvid-NewCrew.mkv", "Unseen Orchard", false},
		{"Requiem.For.A.Dream.DIRECTORS.CUT.2000.720p.BrRip.x264.YIFY.mp4", "Requiem For A Dream", false},
		{"Unseen.Orchard.DIRECTORS.CUT.2007.1080p.BluRay.x264.NewCrew.mkv", "Unseen Orchard", false},
		{"Suits.S03E03.COMPLETE.720p.HDTV.x264-PublicHD", "Suits", true},
		{"House of Cards Season 1 Complete.720p.BRrip.sujaidr (pimprg)", "House of Cards", true},
		{"House of Cards Season1 Complete.720p.BRrip.sujaidr (pimprg)", "House of Cards", true},
		{"Unseen.Orchard.S04E09.COMPLETE.1080p.HDTV.x264-NewCrew.mkv", "Unseen Orchard", true},
		{"Unseen Orchard Season4 Complete.720p.BRrip.NewCrew", "Unseen Orchard", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			release, legacy := RecognizeRelease(tc.raw), Recognize(tc.raw)
			if release.Title != tc.title || legacy.Title != tc.title || release.RawName != tc.raw || legacy.RawName != tc.raw {
				t.Fatalf("title/raw mismatch: %+v / %+v", release, legacy)
			}
			if tc.collection {
				if release.MediaType != TypeTV || release.Season != 0 || release.AbsoluteEpisode != 0 || len(release.Episodes) != 0 || len(release.UnitKeys) != 0 || release.IsAnime || release.Confidence != 0 || !release.RequiresReview || release.Reason != "multi_unit_collection" {
					t.Fatalf("TV COMPLETE gained authority: %+v", release)
				}
				if legacy.Confidence != 0 || !legacy.RequiresReview || legacy.Identity != nil {
					t.Fatalf("legacy gained authority: %+v", legacy)
				}
			} else {
				base := recognizeFilenameEvidence(tc.raw)
				if legacy.Year != base.Year || legacy.Confidence != base.Confidence || legacy.RequiresReview != base.RequiresReview || !reflect.DeepEqual(legacy.Identity, base.Identity) || !reflect.DeepEqual(legacy.Quality, base.Quality) || !reflect.DeepEqual(release.Quality, recognizeReleaseEvidence(tc.raw).Quality) {
					t.Fatal("display-only view changed evidence")
				}
			}
		})
	}
}

func TestScoutRepairNegativeControlsAtBothAPIs(t *testing.T) {
	for _, raw := range []string{
		"UnknownCrew-Orchard.2007.720p.BluRay.x264.mkv",
		"UnknownCrew-Orchard.xvid-NewCrew.mkv",
		"UnknownCrew-Orchard.DIRECTORS.CUT.2007.1080p.BluRay.x264.NewCrew.mkv",
		"Orchard.xvid-NewCrew", "Orchard.xvidUnknown-NewCrew.avi",
		"Orchard.Directors.Cut.2007.mkv", "Orchard.Directors.Cut.2007.720p.Unknown.x264.mkv",
		"Orchard.2007.xvid-NewCrew.avi",
		"Orchard.S01E01.S02E02.COMPLETE.720p.HDTV.x264-NewCrew.mkv",
		"Orchard.1x02.S02E02.COMPLETE.720p.HDTV.x264-NewCrew.mkv",
		"Orchard.S01E01/Other.S02E02.COMPLETE.720p.HDTV.x264-NewCrew.mkv",
		"Orchard.S01E01/Other.xvid-NewCrew.avi",
		"Orchard.S01E01/Other.DIRECTORS.CUT.2007.720p.BRrip.x264.NewCrew.mkv",
		"Orchard.S01E01.COMPLETE/Other.720p.HDTV.x264-NewCrew.mkv",
		"Orchard.Season1.COMPLETE.S02E02.720p.BRrip.NewCrew.mkv",
		"Orchard.Season1.Complete.Unknown.BRrip.NewCrew.mkv",
		"Orchard.S01E01.SAMPLE.720p.HDTV.x264-NewCrew.mkv",
		"Orchard.2007.COMPLETE.BLURAY-NewCrew",
		"Orchard.2007.COMPLETE.BDR-NewCrew",
	} {
		t.Run(raw, func(t *testing.T) {
			r, l := RecognizeRelease(raw), Recognize(raw)
			if !reflect.DeepEqual(r, scoutRepairRelease(raw, r)) || !reflect.DeepEqual(l, scoutRepairFilename(raw, l)) {
				t.Fatal("repair is not idempotent")
			}
			// RecognizeRelease's underlying movie fallback uses Recognize; compare
			// the raw helper directly as well so recursive fallback cannot mask a view.
			if title, ok := scoutRepairTechnicalTitle(raw); ok {
				if title != "UnknownCrew-Orchard" {
					t.Fatalf("unexpected title view %q", title)
				}
			} else if !scoutRepairMixedComplete(raw) && !reflect.DeepEqual(l, recognizeFilenameEvidence(raw)) {
				t.Fatal("negative changed legacy result")
			}
			if _, ok := scoutRepairCompleteTitle(raw); ok {
				t.Fatal("negative gained TV COMPLETE rule")
			}
			if !scoutRepairMixedComplete(raw) && !reflect.DeepEqual(r, recognizeReleaseEvidence(raw)) {
				t.Fatal("negative changed release result")
			}
			if ambiguousEpisodeRegex.MatchString(raw) {
				if r.Confidence != 0 || !r.RequiresReview || len(r.UnitKeys) != 0 || len(r.Episodes) != 0 || r.AbsoluteEpisode != 0 || !l.RequiresReview || l.Identity != nil || l.Confidence > .6 {
					t.Fatalf("mixed/slash/partial evidence gained authority: %+v / %+v", r, l)
				}
			}
			if raw == "Orchard.2007.COMPLETE.BLURAY-NewCrew" || raw == "Orchard.2007.COMPLETE.BDR-NewCrew" {
				if r.MediaType != TypeMovie || r.Confidence != .9 || r.RequiresReview || !reflect.DeepEqual(r.UnitKeys, []string{"movie"}) || l.Confidence != .9 || l.RequiresReview {
					t.Fatalf("single movie disc regressed: %+v / %+v", r, l)
				}
			}
		})
	}
}
