package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"strings"
	"testing"
)

func TestReleaseTitleMetadataCannotHideCoverageOrGroup(t *testing.T) {
	for _, raw := range []string{
		"1883.S01E01.1884.2160p.WEB-DL.x265-ASAP",
		"1883.S01E01.1883.1884.2160p.WEB-DL.x265-ASAP",
		"1883.S01E01.1883.2160p.S01E02.WEB-DL.x265-ASAP",
		"Show.S01E01.1883.2160p.WEB-DL.x265-ASAP",
		"[BEST-TORRENTS.COM] Show.S02E01.Season.02.1080p.WEB-DL.x264-ASAP",
		"[ Torrent911.my ] Show.S02E01.Season.Two.1080p.WEB-DL.x264-ASAP",
		"[BEST-TORRENTS.COM] Show.S02E01.Episode.03.1080p.WEB-DL.x264-ASAP",
	} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || !got.RequiresReview || len(got.UnitKeys) != 0 || got.Quality.Group != "ASAP" {
			t.Errorf("conflicting raw evidence was hidden: %+v", got)
		}
	}
	for _, raw := range []string{
		"[Dr. Who] Show.S02E01.1080p.WEB-DL.x264-ASAP",
		"[My DVD Story] Show.S02E01.1080p.WEB-DL.x264-ASAP",
	} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || got.Title == "Show" || !strings.Contains(got.Title, "Show") || got.Quality.Group != "ASAP" {
			t.Errorf("arbitrary title decoration disappeared: %+v", got)
		}
	}
	for _, raw := range []string{
		"[DVDRip-ITA]The Fast and the Furious: Tokyo Drift [CR-Bt]",
		"[BEST-TORRENTS.COM] The.Penguin.WEBRip.x264",
	} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || got.Year != 0 || len(got.UnitKeys) != 0 || got.RawName != raw {
			t.Errorf("cleanup invented authority: %+v", got)
		}
	}
}
