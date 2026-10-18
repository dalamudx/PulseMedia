package media_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Explicit source collection labels can only rebut coverage. They cannot
// establish the contained units, even when the filename looks like one film.
func validateRakunCollectionRefusal(original *corpusEntry, raw json.RawMessage) error {
	var fields map[string]string
	if err := json.Unmarshal(original.Expected["original_rakun_fields"], &fields); err != nil {
		return fmt.Errorf("missing original Rakun fields: %w", err)
	}
	collection := false
	for _, token := range strings.Fields(fields["meta"]) {
		switch token {
		case "complete", "pack", "has_ovas", "has_specials":
			collection = true
		}
	}
	if !collection {
		return nil
	}
	var w struct {
		Review     bool     `json:"requires_review"`
		Confidence float64  `json:"confidence"`
		Keys       []string `json:"unit_keys"`
		Episodes   []int    `json:"episodes"`
		Season     int      `json:"season"`
		Absolute   int      `json:"absolute_episode"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	if !w.Review || w.Confidence != 0 || w.Season != 0 || w.Absolute != 0 || len(w.Keys) != 0 || len(w.Episodes) != 0 {
		return fmt.Errorf("original collection metadata cannot certify canonical coverage")
	}
	return nil
}

func TestRakunCollectionMetadataRejectsPartialCoverage(t *testing.T) {
	original, err := originalRakunReviewLabels("Death.Parade.1080p.COMPLETE.BluRay.Coffret.Multi-DragonMax")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"requires_review":false,"confidence":0.95,"unit_keys":["movie"]}`,
		`{"requires_review":true,"confidence":0.6}`,
		`{"requires_review":true,"confidence":0,"season":1}`,
		`{"requires_review":true,"confidence":0,"episodes":[1]}`,
		`{"requires_review":true,"confidence":0,"absolute_episode":1}`,
		`{"requires_review":true,"confidence":0,"unit_keys":["S01E01"]}`,
	} {
		if validateRakunCollectionRefusal(original, json.RawMessage(body)) == nil {
			t.Fatal("collection declaration allowed partial coverage", body)
		}
	}
	if err := validateRakunCollectionRefusal(original, json.RawMessage(`{"requires_review":true,"confidence":0}`)); err != nil {
		t.Fatal(err)
	}
	for _, meta := range []string{"complete", "pack", "has_ovas", "has_specials"} {
		labels, err := mappedRakunLiteralLabels(map[string]string{"filename": "Input", "name": "Show", "meta": meta})
		if err != nil {
			t.Fatal(err)
		}
		if validateRakunCollectionRefusal(labels, json.RawMessage(`{"requires_review":true,"confidence":0.6}`)) == nil {
			t.Fatal("collection flag lost", meta)
		}
	}
	labels, err := mappedRakunLiteralLabels(map[string]string{"filename": "Input", "name": "Show", "meta": "not_complete high_quality v2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRakunCollectionRefusal(labels, json.RawMessage(`{"requires_review":true,"confidence":0.6}`)); err != nil {
		t.Fatal("ordinary metadata misread as coverage", err)
	}
}

func TestRakunAudioLayoutRemainsOriginalEvidence(t *testing.T) {
	original, err := originalRakunReviewLabels("Neon Genesis Evangelion HEVC [BDRip.VOSTFR.1080p.x265.DD5.1.AAC]-YOMI")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal(original.Expected["original_rakun_fields"], &fields); err != nil {
		t.Fatal(err)
	}
	if fields["codecs"] != "aac audio_5_1 hevc x265" || string(original.Expected["video_codec"]) != `"H.265"` {
		t.Fatal("original audio/video declarations changed")
	}
	for _, key := range []string{"audio", "audio_channels", "channels"} {
		if _, exists := original.Expected[key]; exists {
			t.Fatal("raw layout became a measured audio fact", key)
		}
	}
	for _, codecs := range []string{"audio_9_1", "audio_5_1 x264 x265"} {
		if _, err := mappedRakunLiteralLabels(map[string]string{"filename": "Input", "name": "Show", "codecs": codecs}); err == nil {
			t.Fatal("unknown layout or conflicting video codecs admitted", codecs)
		}
	}
}
