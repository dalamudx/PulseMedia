package quality_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestQualityPolicyScoring(t *testing.T) {
	policy := quality.DefaultPolicy()

	profile4K := quality.Profile{
		Resolution: quality.Res2160p,
		Source:     quality.SourceBluray,
		HDR:        quality.HDRDolbyVision,
	}

	profile1080p := quality.Profile{
		Resolution: quality.Res1080p,
		Source:     quality.SourceWEBDL,
		HDR:        quality.HDRSDR,
	}

	score4K := policy.Score(profile4K)
	score1080p := policy.Score(profile1080p)

	if score4K <= score1080p {
		t.Fatalf("expected 4K score (%d) > 1080p score (%d)", score4K, score1080p)
	}

	if policy.Compare(profile4K, profile1080p) <= 0 {
		t.Fatal("expected profile4K > profile1080p")
	}
}

func TestParseResolution(t *testing.T) {
	tests := []struct {
		input    string
		expected quality.Resolution
	}{
		{"Inception.2010.2160p.UHD.Remux", quality.Res2160p},
		{"Fight.Club.1999.1080p.BluRay.x264", quality.Res1080p},
		{"Matrix.1999.720p.HDTV", quality.Res720p},
		{"Old.Movie.480p.DVD", quality.Res480p},
		{"Unknown.Release.No.Res", ""},
	}

	for _, tt := range tests {
		got := quality.ParseResolution(tt.input)
		if got != tt.expected {
			t.Errorf("ParseResolution(%q) = %q; expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestQualityOrderIsRelativeToPolicyVersion(t *testing.T) {
	large := quality.Profile{Resolution: quality.Res2160p}
	small := quality.Profile{Resolution: quality.Res1080p}
	originalLarge, originalSmall := large, small
	original := quality.DefaultPolicy()
	bandwidth := quality.Policy{
		ID: "bandwidth", Revision: 2,
		ResolutionOrder: map[quality.Resolution]int{quality.Res2160p: 1, quality.Res1080p: 2},
	}
	if original.Compare(large, small) <= 0 || bandwidth.Compare(large, small) >= 0 {
		t.Fatal("same observed profiles must support opposite versioned policy orders")
	}
	if large != originalLarge || small != originalSmall {
		t.Fatal("policy scoring changed observed quality facts")
	}
	if original.Compare(large, small) <= 0 {
		t.Fatal("evaluating another policy changed the original ordering")
	}
}
