package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

// Original MIT PTN fixture contains both WEB and DVD markers. Ignoring an
// unsupported format is not proof that the supported competing token is true.
func TestUnsupportedQualityMarkersCannotLeaveCompetingAllowedFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source quality.Source
		codec  quality.Codec
	}{
		{"Hercules (2014) WEBDL DVDRip XviD-MAX", "", ""},
		{"Movie.2020.1080p.WEB-DL.DVDRip.x264", "", quality.CodecH264},
		{"Movie.2020.1080p.BluRay.Xvid.HEVC", quality.SourceBluray, ""},
		{"Movie.2020.1080p.WEB-DL.DivX.x264", quality.SourceWEBDL, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := media.RecognizeRelease(tc.name).Quality
			if p.Source != tc.source || p.Codec != tc.codec {
				t.Fatalf("unsupported competing marker silently promoted allowed quality: %+v", p)
			}
		})
	}
}
