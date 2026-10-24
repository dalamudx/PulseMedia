package naming_test

import (
	"math"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
)

func TestPrimaryMediaPathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		eligible bool
		reason   string
	}{
		{"Movie.2024.Sample.mkv", false, "sample_or_trailer"},
		{"Movie.2024.[SAMPLE].MKV", false, "sample_or_trailer"},
		{"Samples/Movie.2024.mkv", false, "sample_or_trailer"},
		{"release/Trailers/Movie.2024.mp4", false, "sample_or_trailer"},
		{`release\sample\Movie.2024.mkv`, false, "sample_or_trailer"},
		{"release/预告片/Movie.2024.mkv", false, "sample_or_trailer"},
		{"release/样片/Movie.2024.mkv", false, "sample_or_trailer"},
		{"release/Movie.featurette.mp4", false, "sample_or_trailer"},
		{"release/Movie-preview.MKV", false, "sample_or_trailer"},
		{"release/Movie.previews.mp4", false, "sample_or_trailer"},
		{"release/Movie.featurettes.mp4", false, "sample_or_trailer"},
		{"The.Sampler.2024.mkv", true, "eligible"},
		{"Sampleman.2024.mkv", true, "eligible"},
		{"Trailering.2024.mp4", true, "eligible"},
		{"Unpreviewed.2024.mkv", true, "eligible"},
		{"release/Example.2024.1080p.mkv", true, "eligible"},
		{"release/.Movie.mkv", false, "hidden_or_invalid_path"},
		{".hidden/Movie.mkv", false, "hidden_or_invalid_path"},
		{"release/../Movie.mkv", false, "hidden_or_invalid_path"},
		{"release/movie\x00.mkv", false, "invalid_media_path"},
		{"release/movie.nfo", false, "non_video_extension"},
		{"release/movie.srt", false, "non_video_extension"},
		{"release/movie.mkv.exe", false, "non_video_extension"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := naming.IsPrimaryMediaPath(tc.name)
			if ok != tc.eligible || reason != tc.reason {
				t.Fatalf("eligibility=%v reason=%q", ok, reason)
			}
		})
	}
}

func TestMediaSizeMustBeMeasuredAndMeetThreshold(t *testing.T) {
	for _, tc := range []struct {
		name          string
		size, minimum int64
		eligible      bool
		reason        string
	}{
		{"unknown", 0, 1, false, "invalid_file_size"},
		{"negative", -1, 1, false, "invalid_file_size"},
		{"under", 199, 200, false, "below_size_threshold"},
		{"at", 200, 200, true, "eligible"},
		{"above", 201, 200, true, "eligible"},
		{"maximum", math.MaxInt64, math.MaxInt64, true, "eligible"},
		{"invalid_configuration", 200, -1, false, "invalid_size_threshold"},
		{"default_under", naming.DefaultMinMediaBytes - 1, 0, false, "below_size_threshold"},
		{"default_at", naming.DefaultMinMediaBytes, 0, true, "eligible"},
		{"explicit_one", 1, 1, true, "eligible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := naming.IsEligibleMediaFile("Movie.2024.mkv", tc.size, tc.minimum)
			if ok != tc.eligible || reason != tc.reason {
				t.Fatalf("eligibility=%v reason=%q", ok, reason)
			}
		})
	}
	// A filename-only decision cannot stand in for measured bytes at execution.
	if ok, _ := naming.IsPrimaryMediaPath("Movie.2024.mkv"); !ok {
		t.Fatal("path-only predicate needs no invented size")
	}
	if ok, _ := naming.IsEligibleMediaFile("Movie.2024.sample.mkv", math.MaxInt64, 1); ok {
		t.Fatal("large sample accepted")
	}
}
