package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestRawRecognitionEvidencePreservesCompleteInput(t *testing.T) {
	for _, raw := range []string{
		"Черное зеркало / Black Mirror / Сезон 4 / Серии 1-6 (6) [2017, США, WEBRip 1080p] MVO + Eng Sub",
		"Show.S01E01 / Film.2020.1080p.WEB-DL.mkv",
		"Show.2024-01-02 / Film.2020.1080p.WEB-DL.mkv",
		"[www.sample.org] Film.2020.1080p.WEB-DL.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if got.RawName != raw || media.Recognize(raw).RawName != raw {
			t.Fatal("original evidence truncated or normalized", raw, got)
		}
	}
}
