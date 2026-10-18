package media_test

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// Upstream MIT annotations are independent partial labels. They can refute a
// recognized source or codec; they cannot require automatic execution, nor
// count as complete title/quality/coverage/confidence golden samples.
func TestMITRecognizedQualityDoesNotContradictIndependentFields(t *testing.T) {
	var sourceLabels, codecLabels int
	for _, entry := range readMITCorpora(t) {
		parsed := media.RecognizeRelease(entry.Name).Quality
		if raw, ok := entry.Expected["source"]; ok {
			sourceLabels++
			var label string
			if err := json.Unmarshal(raw, &label); err != nil {
				t.Fatalf("source label %q: %v", entry.Name, err)
			}
			allowed := map[string][]quality.Source{
				"Blu-ray": {quality.SourceBluray, quality.SourceRemux},
				"Web":     {quality.SourceWEBDL, quality.SourceWEBRip},
				"HDTV":    {quality.SourceHDTV},
				"DVD":     {}, "Telesync": {}, "Camera": {}, "Telecine": {},
				"Screener": {}, "VHS": {}, "Satellite": {},
			}
			sources, known := allowed[label]
			if !known {
				t.Fatalf("unreviewed source label %q: %q", entry.Name, label)
			}
			if parsed.Source != "" {
				valid := false
				for _, source := range sources {
					if parsed.Source == source {
						valid = true
					}
				}
				if !valid {
					t.Errorf("source contradicts upstream %q: observed %q, label %q", entry.Name, parsed.Source, label)
				}
			}
		}
		if raw, ok := entry.Expected["video_codec"]; ok {
			codecLabels++
			var label string
			if err := json.Unmarshal(raw, &label); err != nil {
				t.Fatalf("codec label %q: %v", entry.Name, err)
			}
			codecs := map[string]quality.Codec{"H.264": quality.CodecH264, "H.265": quality.CodecHEVC, "Xvid": ""}
			expected, known := codecs[label]
			if !known {
				t.Fatalf("unreviewed codec label %q: %q", entry.Name, label)
			}
			if parsed.Codec != "" && parsed.Codec != expected {
				t.Errorf("codec contradicts upstream %q: observed %q, label %q", entry.Name, parsed.Codec, label)
			}
		}
	}
	if sourceLabels != 376 || codecLabels != 360 {
		t.Fatalf("labeled field count changed; review independent source: %d / %d", sourceLabels, codecLabels)
	}
}
