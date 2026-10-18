package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestNextAArchiveScopeRefusalBothAPIs(t *testing.T) {
	cases := []string{
		"Silver.Grove.2006.1080p.BluRay.x264-NewCrew.part01.rar",
		"Copper.Valley.2018.720p.WEB-DL.x265-OtherCrew.PART002.RAR",
		"[UnknownPrefix] Amber.River.2004.1080p.DVD.x264-NewCrew.part03.rar",
		"Silver.Grove.2006.{tmdb-891}.1080p.BluRay.x264-NewCrew.part01.rar",
		"Cedar.S02E07.1080p.WEBRip.x264-NewCrew.part01.rar",
		"Cedar.2x07.1080p.WEBRip.x264-NewCrew.part01.rar",
		"[NewCrew] Cedar - 07 [1080p][abcdef12].part01.rar",
		"Cedar.2004.1080p.x264-NewCrew.part01.rar/Clean.2011.{tmdb-891}.mkv",
		"Cedar.2004.1080p.x264-NewCrew.part01.rar/Clean.S02E04.mkv",
		`Cedar.2004.1080p.x264-NewCrew.part01.rar\Clean.2011.{tmdb-891}.mkv`,
		"Cedar.S02E07.1080p.x264-NewCrew.part01.rar/Clean.2x09.mkv",
		"Cedar.S02E07.1080p.x264-NewCrew.part01.rar/[NewCrew] Clean - 09 [1080p][abcdef12].mkv",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			r, m := media.RecognizeRelease(raw), media.Recognize(raw)
			if r.Confidence != 0 || !r.RequiresReview || len(r.UnitKeys) != 0 || len(r.Episodes) != 0 || r.Season != 0 || r.AbsoluteEpisode != 0 || r.IsAnime || m.Identity != nil || m.Confidence != 0 || !m.RequiresReview || r.RawName != raw || m.RawName != raw {
				t.Fatalf("archive authority leaked: %+v %+v", r, m)
			}
		})
	}
}
func TestNextAArchiveLexicalBoundaries(t *testing.T) {
	for _, raw := range []string{"Part.01.1998.1080p.x264-NewCrew.mkv", "Copper.2011.1080p.x264-NewCrew.part01.rarx", "Copper.2011.1080p.x264-NewCrew.partx.rar"} {
		r := media.RecognizeRelease(raw)
		if r.Reason == "partial_movie_content" {
			t.Fatalf("invented archive syntax: %s %+v", raw, r)
		}
	}
	for _, raw := range []string{"Silver.Grove.2006.{tmdb-891}.1080p.x264-NewCrew.part01.rar.srt", "Copper.S02E07.1080p.WEBRip.x264-NewCrew.part01.rar.txt"} {
		r, m := media.RecognizeRelease(raw), media.Recognize(raw)
		if r.Confidence != 0 || !r.RequiresReview || len(r.UnitKeys) != 0 || m.Identity != nil || m.Confidence != 0 {
			t.Fatalf("sidecar acquired identity: %+v %+v", r, m)
		}
	}
}
