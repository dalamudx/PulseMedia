package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestSourceCodecTokensNeverInferFromTitleSubstrings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source quality.Source
		codec  quality.Codec
	}{
		{"BlueRay.2020.1080p.Adventureland.x264ish", "", ""},
		{"WebRipples.2020.1080p.H2640", "", ""},
		{"Show.2020.S01E01.1080p.BluRay.x265", quality.SourceBluray, quality.CodecHEVC},
		{"Show.2020.S01E01.1080p.Blu-Ray.H.264", quality.SourceBluray, quality.CodecH264},
		{"Show.2020.S01E01.1080p.WEB-DL.AV1", quality.SourceWEBDL, quality.CodecAV1},
		{"Show.2020.S01E01.1080p.BluRay.Remux.HEVC", quality.SourceRemux, quality.CodecHEVC},
		{"Show.2020.S01E01.1080p.WEB-DL.WEBRip.x264", "", quality.CodecH264},
		{"Show.2020.S01E01.1080p.WEB-DL.HEVC.x264", quality.SourceWEBDL, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := media.RecognizeRelease(tc.name).Quality
			if p.Source != tc.source || p.Codec != tc.codec {
				t.Fatalf("unexpected facts: %+v", p)
			}
			if (p.Source != "") != (p.SourceParserVersion == quality.SourceTokenParserVersion) || (p.Codec != "") != (p.CodecParserVersion == quality.CodecTokenParserVersion) {
				t.Fatalf("missing provenance: %+v", p)
			}
		})
	}
}
