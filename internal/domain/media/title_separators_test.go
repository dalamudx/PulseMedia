package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

// Synthetic boundary probes are not part of the independently sourced count.
func TestDistributionSiteCannotSupplyIdentityCoverageOrQuality(t *testing.T) {
	for _, raw := range []string{
		"www.2024.com - Bare.Work",
		"www,2024.com - Bare.Work",
		"ww.2024.com - Bare.Work",
		"[www.2024.com] Bare.Work",
	} {
		got := media.RecognizeRelease(raw)
		movie := media.Recognize(raw)
		if got.RawName != raw || got.Year != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || movie.Year != 0 || !movie.RequiresReview || movie.RawName != raw {
			t.Fatalf("domain manufactured work year: %+v %+v", got, movie)
		}
	}
	for _, raw := range []string{
		"www.S02E03.com - Film.2020.1080p.BluRay.H264-GRP.mkv",
		"www.2024-01-02.com - Film.2020.1080p.BluRay.H264-GRP.mkv",
		"www.x265.com - Film.2020.1080p.BluRay.H264-GRP.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || got.Title != "Film" || got.Year != 2020 || got.MediaType != media.TypeMovie || got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" || got.Quality.Codec != quality.CodecH264 || got.Quality.Group != "GRP" {
			t.Fatalf("distribution metadata changed actual work: %+v", got)
		}
	}
	for _, raw := range []string{
		"www.Site.com - Show.S01E01.S02E03.1080p.WEB-DL.x264-GRP.mkv",
		"www.Site.com - Show.S01E01.2.1080p.WEB-DL.x264-GRP.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || !got.RequiresReview || len(got.UnitKeys) != 0 || got.Quality.Group != "GRP" {
			t.Fatalf("site removal hid conflicting work coverage: %+v", got)
		}
	}
}

func TestWorkVersionCannotBecomeYearOrEpisodeCoverage(t *testing.T) {
	for _, c := range []struct {
		raw, title string
		year       int
	}{
		{"M3GAN.2.0.2025.1080p.mkv", "M3GAN 2.0", 2025},
		{"COMPASS.2.0.ANIMATION.PROJECT.S01E02.1080p.mkv", "COMPASS 2.0 ANIMATION PROJECT", 0},
		{"Mr. Nobody", "Mr. Nobody", 0},
		{"1. Детские игры.1988.1080p.mkv", "1. Детские игры", 1988},
		{"01. 100 девчонок и одна в лифте 2000 WEBRip.mkv", "01. 100 девчонок и одна в лифте", 2000},
		{"Mr.Nobody.2009.1080p.mkv", "Mr Nobody", 2009},
		{"Movie.2.2024.1080p.mkv", "Movie 2", 2024},
		{"Movie.20.2024.1080p.mkv", "Movie 20", 2024},
	} {
		got := media.RecognizeRelease(c.raw)
		if got.Title != c.title || got.Year != c.year || got.RawName != c.raw {
			t.Fatalf("work/year separator changed: %+v", got)
		}
		if got.MediaType == media.TypeTV && (len(got.UnitKeys) != 1 || got.UnitKeys[0] != "S01E02") {
			t.Fatalf("version expanded units: %+v", got)
		}
	}
	for _, raw := range []string{"1984.2020.1080p.mkv", "Show.S01E02.2.0.1080p.mkv", "Show.2024.01.02.1080p.mkv"} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatalf("decimal cleanup granted uncertain coverage: %+v", got)
		}
	}
}

func TestExplicitSubtitleAndBracketedSizeAreNotGroups(t *testing.T) {
	for _, tail := range []string{"ESub", "ESubs", "400MB]", "400mb)", "2GiB}"} {
		got := media.RecognizeRelease("Film.2020.1080p.BluRay.H264-" + tail + ".mkv")
		if got.Quality.Group != "" {
			t.Fatalf("metadata supplied release group: %+v", got)
		}
	}
	for _, tail := range []string{"GRP[UTR]", "400MB[UTR]", "SubsPlease"} {
		got := media.RecognizeRelease("Film.2020.1080p.BluRay.H264-" + tail + ".mkv")
		if got.Quality.Group != tail {
			t.Fatalf("unknown or actual group lost: %+v", got)
		}
	}
}

func TestAliasBasenameCannotHideEarlierCoverage(t *testing.T) {
	for _, raw := range []string{
		"Властелин колец: Кольца власти (S1E1-8 of 8) / The Lord of the Rings: The Rings of Power (2022) WEB-DL",
		"Show.S01E01 / Film.2020.1080p.BluRay.H264-GRP.mkv",
		"www.Site.com - Show.S01E01 / Film.2020.1080p.BluRay.H264-GRP.mkv",
		"Show.2024-01-02 / Film.2020.1080p.BluRay.H264-GRP.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatalf("basename hid earlier raw coverage: %+v", got)
		}
	}
}
