package media_test

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestScoutRepairIndependentOriginalContradictions(t *testing.T) {
	original, err := originalSmallScoutReviewLabels("singers-scene-release", "Citizenfour.2014.720p.WEB-DL.AAC2.0.H.264-NOGRP")
	if err != nil {
		t.Fatal(err)
	}
	for field, literal := range map[string]string{"title": "\"Citizenfour\"", "year": "2014", "screen_size": "\"720p\"", "source": "\"Web\"", "video_codec": "\"H.264\"", "group": "\"NOGRP\""} {
		if string(original.Expected[field]) != literal {
			t.Fatalf("original field %s changed: %s", field, original.Expected[field])
		}
	}
	q := quality.Profile{Resolution: "720p", Source: "WEB-DL", Codec: "H264", Group: "NOGRP"}
	if err := validateReviewedQualityLabels(original.Expected, q); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"resolution", "source", "codec", "group"} {
		changed := q
		switch field {
		case "resolution":
			changed.Resolution = "1080p"
		case "source":
			changed.Source = "Bluray"
		case "codec":
			changed.Codec = "H265"
		case "group":
			changed.Group = "OtherCrew"
		}
		if err := validateReviewedQualityLabels(original.Expected, changed); err == nil {
			t.Fatalf("original %s contradiction accepted", field)
		}
	}
	rows, err := smallScoutInventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Source != "singers-scene-release" || (row.Index != 7 && row.Index != 8 && row.Index != 9 && row.Index != 13) {
			continue
		}
		o, err := originalSmallScoutReviewLabels(row.Source, row.Input)
		if err != nil {
			t.Fatal(err)
		}
		if row.Index == 7 && string(o.Expected["year"]) != "1968" {
			t.Fatal("pending original year erased")
		}
		if row.Index != 7 {
			var title string
			if err := json.Unmarshal(o.Expected["title"], &title); err != nil || title != row.Title {
				t.Fatal("pending original numbered title erased")
			}
		}
	}
}

func TestScoutRepairOriginalCompleteRefusesEveryAuthorityField(t *testing.T) {
	original, err := originalSmallScoutReviewLabels("husar-movie-title", "Suits.S03E03.COMPLETE.720p.HDTV.x264-PublicHD")
	if err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]any{"season": 1, "episodes": []int{1}, "unit_keys": []string{"S01E01"}, "absolute_episode": 1, "confidence": 0.95, "requires_review": false} {
		refusal := map[string]any{"season": 0, "episodes": []int{}, "unit_keys": []string{}, "absolute_episode": 0, "confidence": 0, "requires_review": true}
		refusal[field] = value
		raw, err := json.Marshal(refusal)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSmallScoutOriginalRebuttals(original, raw); err == nil {
			t.Fatalf("original COMPLETE accepted %s", field)
		}
	}
}
