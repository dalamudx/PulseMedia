package media_test

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestVP8OriginalClaimRejectsSupportedCodecPromotion(t *testing.T) {
	labels := map[string]json.RawMessage{"ptt_main_codec": json.RawMessage(`"vp8"`)}
	if err := validatePTTMainQualityRebuttals(labels, quality.Profile{}); err != nil {
		t.Fatal("explicit unsupported codec must retain unknown fact", err)
	}
	for _, codec := range []quality.Codec{quality.CodecH264, quality.CodecHEVC} {
		if err := validatePTTMainQualityRebuttals(labels, quality.Profile{Codec: codec}); err == nil {
			t.Fatalf("unsupported original vp8 allowed %s", codec)
		}
	}
}
