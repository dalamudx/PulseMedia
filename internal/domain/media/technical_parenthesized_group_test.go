package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestTechnicalParenthesizedReleaseGroups(t *testing.T) {
	for _, tc := range []struct{ name, group string }{
		{"The Fresh Prince of Bel-Air (1990) S04 (1080p HMAX WEB-DL H265 SDR DD 2.0 English - HONE)", "HONE"},
		{"Allegiant (2016) (1080p BDRip x265 10bit TrueHD Atmos 7.1 - JBENT)[TAoE]", "TAoE"},
		{"Another Show S04 (1080p WEB-DL H265 - ExampleGroup)", "ExampleGroup"},
		{"Another Movie (2016) (1080p BDRip x265 - Encoder)[ExampleGroup]", "ExampleGroup"},
		{"Movie (2024) (YIFY)", ""},
		{"Movie (2024) [RARBG]", ""},
		{"Movie (2024) (1080p - HONE)", ""},
		{"Movie (2024) (1080p BDRip x265 - Encoder)[Unknown][TAoE]", ""},
		{"Movie (2024) (1080p BDRip x265 - Encoder)[IMAX]", ""},
		{"Movie (2024) (1080p BDRip x265 - COMPLETE)", ""},
		{"Movie (2024) (1080p BDRip x265 - Encoder)[TAoE] extra", ""},
		{"Movie (2024) (1080p BDRip VP8 - Encoder)[TAoE]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := media.Recognize(tc.name)
			got := media.RecognizeRelease(tc.name)
			if string(old.Quality.Group) != tc.group || string(got.Quality.Group) != tc.group {
				t.Fatalf("group got legacy=%q normalized=%q, want %q", old.Quality.Group, got.Quality.Group, tc.group)
			}
		})
	}
}
