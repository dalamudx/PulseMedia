package media_test

import (
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"slices"
	"testing"
)

// Expected attributes are review labels, not parser observations: independently
// pinned upstream values can refute them before we compare with PulseMedia.
func validateReviewedQualityLabels(labels map[string]json.RawMessage, q quality.Profile) error {
	sourceValues := map[string][]quality.Source{
		"Blu-ray": {quality.SourceBluray, quality.SourceRemux}, "Web": {quality.SourceWEBDL, quality.SourceWEBRip}, "HDTV": {quality.SourceHDTV},
		"DVD": {}, "Raw": {}, "Telesync": {}, "Camera": {}, "Telecine": {}, "Screener": {}, "VHS": {}, "Satellite": {},
	}
	codecValues := map[string]quality.Codec{"H.264": quality.CodecH264, "H.265": quality.CodecHEVC, "Xvid": "", "MPEG": ""}
	for _, key := range []string{"screen_size", "source", "video_codec", "group"} {
		raw, ok := labels[key]
		if !ok {
			continue
		}
		var label string
		if err := json.Unmarshal(raw, &label); err != nil {
			return fmt.Errorf("invalid independent %s label: %w", key, err)
		}
		switch key {
		case "screen_size":
			if q.Resolution != "" && string(q.Resolution) != label {
				return fmt.Errorf("reviewed resolution %s contradicts independent %s", q.Resolution, label)
			}
		case "group":
			if q.Group != "" && q.Group != label {
				return fmt.Errorf("reviewed group %s contradicts independent %s", q.Group, label)
			}
		case "source":
			values, known := sourceValues[label]
			if !known {
				return fmt.Errorf("unreviewed independent source label %q", label)
			}
			if q.Source != "" && !slices.Contains(values, q.Source) {
				return fmt.Errorf("reviewed source %s contradicts independent %s", q.Source, label)
			}
		case "video_codec":
			value, known := codecValues[label]
			if !known {
				return fmt.Errorf("unreviewed independent codec label %q", label)
			}
			if q.Codec != "" && q.Codec != value {
				return fmt.Errorf("reviewed codec %s contradicts independent %s", q.Codec, label)
			}
		}
	}
	return nil
}

func TestReviewedQualityCannotRewriteIndependentLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels string
		q      quality.Profile
		reject bool
	}{
		{"group mismatch", `{"group":"CHD"}`, quality.Profile{Group: "OTHER"}, true},
		{"group case mismatch", `{"group":"CHD"}`, quality.Profile{Group: "chd"}, true},
		{"malformed group label", `{"group":{"symbol_ref":"Group.CHD"}}`, quality.Profile{}, true},
		{"unknown group stays unknown", `{"group":"BEN.THE.MEN"}`, quality.Profile{}, false},
		{"explicit group matches", `{"group":"CHD"}`, quality.Profile{Group: "CHD"}, false},
		{"source mismatch", `{"source":"HDTV"}`, quality.Profile{Source: quality.SourceBluray}, true},
		{"codec mismatch", `{"video_codec":"H.264"}`, quality.Profile{Codec: quality.CodecHEVC}, true},
		{"unsupported DVD upgrade", `{"source":"DVD"}`, quality.Profile{Source: quality.SourceWEBDL}, true},
		{"unsupported MPEG promotion", `{"video_codec":"MPEG"}`, quality.Profile{Codec: quality.CodecH264}, true},

		{"raw cannot assert a source", `{"source":"Raw"}`, quality.Profile{Source: quality.SourceBluray}, true},
		{"raw stays unknown", `{"source":"Raw"}`, quality.Profile{}, false},
		{"unsupported Xvid promotion", `{"video_codec":"Xvid"}`, quality.Profile{Codec: quality.CodecH264}, true},
		{"resolution promotion", `{"screen_size":"720p"}`, quality.Profile{Resolution: quality.Res1080p}, true},
		{"unknown source label", `{"source":"Unreviewed"}`, quality.Profile{}, true},
		{"malformed codec label", `{"video_codec":true}`, quality.Profile{}, true},
		{"unknown codec label", `{"video_codec":"H.999"}`, quality.Profile{}, true},
		{"bluray family remux", `{"source":"Blu-ray"}`, quality.Profile{Source: quality.SourceRemux}, false},
		{"web family rip", `{"source":"Web"}`, quality.Profile{Source: quality.SourceWEBRip}, false},
		{"unsupported stays unknown", `{"source":"DVD","video_codec":"Xvid"}`, quality.Profile{}, false},
		{"partial unknown not invented", `{"screen_size":"1080p","source":"Web"}`, quality.Profile{}, false},
		{"explicit matches", `{"screen_size":"1080p","source":"HDTV","video_codec":"H.264"}`, quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceHDTV, Codec: quality.CodecH264}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var labels map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.labels), &labels); err != nil {
				t.Fatal(err)
			}
			if err := validateReviewedQualityLabels(labels, tc.q); (err != nil) != tc.reject {
				t.Fatal("independent quality refutation boundary", err, tc.reject)
			}
		})
	}
}
