package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"strings"
	"testing"
)

func TestRLSClearRepairFullRawRefusals(t *testing.T) {
	for _, raw := range []string{
		"Movie.2024.SUBPACK.BluRay.x264-GRP",
		"Movie.2024.SUBPACK.BluRay/Feature.2023.{tmdb-123}.mkv",
		"Movie.2024.SUBPACK.BluRay/Show.S01E01.1080p.mkv",
		"Movie.2024.SUBPACK.BluRay/[SubsPlease] Show - 03 [1080p].mkv",
		"Movie.2024.Digital.Extras.1080p.WEB-DL.x264-GRP",
		"Movie.Extras.Only.2024.1080p.BluRay.x264-GRP",
		"Movie.2024.Extras.1080p/Show.S01E01.mkv",
		"Movie.2024.SUBPACK.BluRay/Feature.2023.srt",
		"Show.Complete.Series/Feature.2024.{tmdb-123}.mkv",
		"Show.Complete.Series/Show.S01E01.1080p.mkv",
	} {
		t.Run(raw, func(t *testing.T) {
			got, old := media.RecognizeRelease(raw), media.Recognize(raw)
			if got.RawName != raw || got.Confidence != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.Season != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
				t.Fatalf("raw refusal escaped: %+v", got)
			}
			if old.RawName != raw || old.Identity != nil || old.Confidence != 0 || !old.RequiresReview || media.MatchesAutomaticMovie(raw, "Feature", 2023, map[string]string{"tmdb.movie": "123"}) {
				t.Fatalf("legacy refusal escaped: %+v", old)
			}
		})
	}
}

func TestRLSClearRepairConservativeBoundaries(t *testing.T) {
	for _, raw := range []string{
		"German.2014.1080p.BluRay.x264-GRP",
		"The.Good.German.2006.1080p.BluRay.x264-GRP",
		"Extras.2005.1080p.BluRay.x264-GRP",
		"SUBPACK.2024.mkv",
		"IMAX.2024.1080p.BluRay.x264-GRP",
		"Unknown.Movie.2024.1080p.BluRay.x264-UnknownGroup",
		"[UnknownProvider] Movie.2024.1080p.BluRay.x264-GRP",
	} {
		got := media.RecognizeRelease(raw)
		if got.RequiresReview || len(got.UnitKeys) != 1 || got.RawName != raw {
			t.Errorf("ordinary movie harmed: %+v", got)
		}
		if strings.Contains(raw, "German") && !strings.Contains(got.Title, "German") {
			t.Errorf("ordinary German title erased: %+v", got)
		}
		if strings.Contains(raw, "UnknownProvider") && !strings.Contains(got.Title, "UnknownProvider") {
			t.Errorf("unknown bracket erased: %+v", got)
		}
	}
	for _, raw := range []string{
		"Movie.2014.2015.1080p.BluRay.x264-GRP",
		"Show.S01/Feature.2024.1080p.mkv",
		"Show.S01E01.S02E02.1080p.mkv",
		"[SubsPlease] Show - 03 [1080p].mkv",
	} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || len(got.UnitKeys) > 0 {
			t.Errorf("uncertain scope gained authority: %+v", got)
		}
	}
}

// Synthetic mutation controls are not independent gold annotations.
func TestRLSClearRepairDiscScopesCannotHideBehindPublicHooks(t *testing.T) {
	for _, scope := range []string{
		"Show.1993.S01S02.COMPLETE.x264-GRP",
		"Show.S01D02.COMPLETE.BLURAY-GRP",
		"Show.2005.Series.1.Disk.2.of.2.ISO.DVDR-GRP",
	} {
		for _, suffix := range []string{"", "/Feature.2024.{tmdb-123}.mkv", "/Show.S01E01.1080p.mkv", "/Show.1x01.1080p.mkv", "/[SubsPlease] Show - 03 [1080p].mkv", "/Feature.2024.srt"} {
			raw := scope + suffix
			got, old := media.RecognizeRelease(raw), media.Recognize(raw)
			if got.RawName != raw || got.Confidence != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.Season != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
				t.Errorf("disc scope escaped: %+v", got)
			}
			if old.RawName != raw || old.Identity != nil || old.Confidence != 0 || !old.RequiresReview {
				t.Errorf("legacy disc escaped: %+v", old)
			}
		}
	}
}
