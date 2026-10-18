package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestNextBRepairUnseenLexicalBoundaries(t *testing.T) {
	for _, tc := range []struct{ raw, title, group string }{
		{"Cedar.Clouds.2008.1080p.x264-Silver-Bridge.mkv", "Cedar Clouds", "Silver-Bridge"},
		{"Copper.Garden.S02E09.1080p.HEVC-[NewTeam]", "Copper Garden", "NewTeam"},
		{"Amber.Valley.S03E11.1080p.H264-SkyPool-Wawacity.win.mkv", "Amber Valley", "SkyPool-Wawacity.win"},
		{"Ochre (Vega) S02E03 1080p Web H264 @@MoonRiver@@", "Ochre (Vega)", "@@MoonRiver@@"},
		{"Jade.Path.S02E04.1080p.x264.AC3.2.0.@NewGroup", "Jade Path", "@NewGroup"},
		{"Cobalt (2006) S02E03 2160p.HDR+Dolby.Vision.h265.⚡Night-River⚡.mkv", "Cobalt", "⚡Night-River⚡"},
		{"Aster.2008.1080p.BluRay.REMUX.MULTi-New.Tree.Group", "Aster", "New.Tree.Group"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			r, m := media.RecognizeRelease(tc.raw), media.Recognize(tc.raw)
			if r.Title != tc.title || r.Quality.Group != tc.group || m.Quality.Group != tc.group {
				t.Fatalf("release=%+v movie=%+v", r, m)
			}
			if r.RawName != tc.raw || m.RawName != tc.raw {
				t.Fatal("raw changed")
			}
		})
	}
	for _, tc := range []struct{ raw, title string }{
		{"Silver.Prairie.S03.1080p.x265.rar", "Silver Prairie"},
		{"Copper-19.S02.1080p.SDR.H265.7z", "Copper-19"},
		{"[NightHost] WebRip Dreams S02 1080p H265.7z", "[NightHost] WebRip Dreams"},
		{"HDR Kingdom S03 720p H264.rar", "HDR Kingdom"},
		{"[NightHost] WebRip Dreams (2007) S02 1080p H265.7z", "[NightHost] WebRip Dreams"},
		{"HDR Kingdom (2007) S03 720p H264.rar", "HDR Kingdom"},
		{"Amber.Tower.E09.MULTi.1080p.WEB.x264-NewGroup", "Amber Tower"},
		{"Topaz.River.S03.COMPLETE.720p.AMZN.WEBRip.x264-NewGroup", "Topaz River"},
		{"Lilac.S04.COMPLETE.2160p.NF.WEB-DL.H265", "Lilac"},
		{"Opal Range E01-E03 2007 1080p Blu-ray x265 AC3￡Night@Team", "Opal Range"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			r, m := media.RecognizeRelease(tc.raw), media.Recognize(tc.raw)
			if r.Title != tc.title || m.Title != tc.title || !r.RequiresReview || r.Confidence != 0 || len(r.Episodes) != 0 || len(r.UnitKeys) != 0 || r.AbsoluteEpisode != 0 || r.Season != 0 || !m.RequiresReview || m.Confidence != 0 || m.Identity != nil {
				t.Fatalf("release=%+v movie=%+v", r, m)
			}
		})
	}
}
func TestNextBRepairExplicitUnderscoreCoordinates(t *testing.T) {
	for _, raw := range []string{"Silver_Garden_S03E09_-_Evening_Walk.mkv", "Silver_Garden_-_S03E09_-_Evening_Walk.mkv"} {
		r := media.RecognizeRelease(raw)
		if r.Title != "Silver Garden" || r.RequiresReview || r.Confidence != .95 || len(r.UnitKeys) != 1 || r.UnitKeys[0] != "S03E09" || r.Quality.Group != "" {
			t.Fatalf("%+v", r)
		}
	}
}
func TestNextBRepairFailClosedControls(t *testing.T) {
	// Complete raw parent evidence must remain visible to both public entry points.
	for _, prefix := range []string{"Night.Cedar.S02.COMPLETE.720p.AMZN.WEBRip.x264-T", "Night Cedar E01-E03 2007 1080p Blu-ray x265", "Night Cedar S01E14A 720p HDTV x264-G"} {
		for _, tail := range []string{"Film.2007.{tmdb-42}.1080p.WEB-DL.mkv", "Film.S02E01.720p.mkv", "Film.2x01.720p.mkv", "[SubsPlease] Film - 03 [720p].mkv"} {
			raw := prefix + "/" + tail
			r, m := media.RecognizeRelease(raw), media.Recognize(raw)
			if r.RawName != raw || m.RawName != raw || len(r.UnitKeys) != 0 || len(r.Episodes) != 0 || r.AbsoluteEpisode != 0 || r.Confidence != 0 || !r.RequiresReview || m.Identity != nil || m.Confidence != 0 || !m.RequiresReview {
				t.Fatalf("mixed evidence lost: %q release=%+v movie=%+v", raw, r, m)
			}
		}
	}
	for _, ext := range []string{".srt", ".txt", ".zip"} {
		raw := "Night.Cedar.2007.{tmdb-42}.1080p.WEB-DL" + ext
		r, m := media.RecognizeRelease(raw), media.Recognize(raw)
		if r.Reason != "non_video_file" || !r.RequiresReview || r.Confidence != 0 || len(r.UnitKeys) != 0 || m.Identity != nil || m.Confidence != 0 || !m.RequiresReview || r.RawName != raw || m.RawName != raw {
			t.Fatalf("nonvideo %s: %+v %+v", ext, r, m)
		}
	}
	for _, raw := range []string{"Night.Cedar.S03E09.1080p.MyHDR+Dolbyish.Vision.x265-G", "Night.Cedar.S03E09.1080p.HDR+Dolby.Visionary.x265-G"} {
		r := media.RecognizeRelease(raw)
		if r.Quality.HDR == quality.HDRDolbyVision {
			t.Fatalf("substring gained HDR: %+v", r)
		}
	}
	r := media.RecognizeRelease("Cedar.S00E66.720p.WEB-DL.H264-NewGroup")
	if len(r.UnitKeys) != 1 || r.UnitKeys[0] != "S00E66" || r.RequiresReview {
		t.Fatalf("canonical S00 reclassified: %+v", r)
	}
}

func TestNextBRepairSymbolPublisherScope(t *testing.T) {
	for _, tc := range []struct{ raw, group string }{
		{"Unseen.2024.1080p.BluRay.x265.@@NewPublisher@@", ""},
		{"Unseen.2024.1080p.BluRay.x265.AC3.2.0.@NewPublisher", ""},
		{"Unseen.2024.S03E09.1080p.BluRay.x265.@@NewPublisher@@", "@@NewPublisher@@"},
	} {
		release, movie := media.RecognizeRelease(tc.raw), media.Recognize(tc.raw)
		if release.Quality.Group != tc.group || movie.Quality.Group != tc.group || release.RawName != tc.raw || movie.RawName != tc.raw {
			t.Fatalf("symbolic publisher context mismatch: %+v / %+v", release, movie)
		}
	}
}
