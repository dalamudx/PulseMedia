package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

// Da Vinci Code DVDRip is an original fixed MIT PTT corpus input.
// A DVD token is not a Dolby Vision token. These explicit grammar contracts
// are partial safety annotations, not complete independent golden approval.
func TestQualityFactsRequireExplicitTokens(t *testing.T) {
	for _, s := range []string{"Da Vinci Code DVDRip", "Adventureland.2009.1080p.WEB-DL.x264-GROUP", "Film.2020.DVD.1080p.x264", "Film.2020.DVDScr.x264", "Film.2020.HDAdventure.1080p.x264"} {
		r := media.RecognizeRelease(s)
		if r.Quality.HDR != "" {
			t.Errorf("implicit HDR invented for %q: %+v", s, r.Quality)
		}
		legacy := media.Recognize(s)
		if legacy.Quality.HDR != "" {
			t.Errorf("legacy false HDR %q: %+v", s, legacy.Quality)
		}
	}
	for _, c := range []struct {
		s   string
		hdr quality.HDR
	}{{"Film.2020.DV.2160p.WEB-DL.x265", quality.HDRDolbyVision}, {"Film.2020.DoVi.2160p.WEB-DL.x265", quality.HDRDolbyVision}, {"Film.2020.HDR10+.2160p.WEB-DL.x265", quality.HDR10Plus}, {"Film.2020.HDR10.2160p.WEB-DL.x265", quality.HDR10}, {"Film.2020.SDR.1080p.WEB-DL.x264", quality.HDRSDR}} {
		if r := media.RecognizeRelease(c.s); r.Quality.HDR != c.hdr {
			t.Errorf("explicit fact lost %q: %+v", c.s, r.Quality)
		}
	}
}
