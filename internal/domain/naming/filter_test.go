package naming_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
)

func TestIsEligibleMediaFileRules(t *testing.T) {
	minSize := int64(200 * 1024 * 1024) // 200MB

	// 1. Valid 8GB movie file -> eligible
	ok, reason := naming.IsEligibleMediaFile("Fight.Club.1999.1080p.mkv", 8*1024*1024*1024, minSize)
	if !ok || reason != "eligible" {
		t.Fatalf("expected eligible, got: %v, %s", ok, reason)
	}

	// 2. Sample file -> rejected
	okSample, reasonSample := naming.IsEligibleMediaFile("Fight.Club.1999.sample.mkv", 50*1024*1024, minSize)
	if okSample || reasonSample != "sample_or_trailer" {
		t.Fatalf("expected sample rejected, got: %v, %s", okSample, reasonSample)
	}

	// 3. Trailer file -> rejected
	okTrailer, reasonTrailer := naming.IsEligibleMediaFile("Inception.2010.official.trailer.mp4", 150*1024*1024, minSize)
	if okTrailer || reasonTrailer != "sample_or_trailer" {
		t.Fatalf("expected trailer rejected, got: %v, %s", okTrailer, reasonTrailer)
	}

	// 4. Non-video file (.nfo, .txt, .exe) -> rejected
	okNFO, reasonNFO := naming.IsEligibleMediaFile("movie.nfo", 2048, minSize)
	if okNFO || reasonNFO != "non_video_extension" {
		t.Fatalf("expected nfo rejected, got: %v, %s", okNFO, reasonNFO)
	}

	// 5. Tiny clip below 200MB threshold -> rejected
	okTiny, reasonTiny := naming.IsEligibleMediaFile("Clip.mkv", 50*1024*1024, minSize)
	if okTiny || reasonTiny != "below_size_threshold" {
		t.Fatalf("expected below threshold rejected, got: %v, %s", okTiny, reasonTiny)
	}
}
