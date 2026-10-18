package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestTechnicalRipSuffixCannotPolluteWorkTitle(t *testing.T) {
	for _, tc := range []struct{ raw, title string }{
		{"Da Vinci Code DVDRip", "Da Vinci Code"},
		{"Feature.BDRip.x264", "Feature"},
		{"Feature.BRRip.XviD", "Feature"},
		{"Feature.HDRip.x264", "Feature"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := media.RecognizeRelease(tc.raw)
			if got.Title != tc.title || got.Year != 0 || got.Confidence != .6 || !got.RequiresReview || len(got.UnitKeys) != 0 || got.Reason != "title_only_without_year" {
				t.Fatalf("technical suffix acquired title/identity authority: %+v", got)
			}
		})
	}
}

func TestUnsupportedRipTitleCleanupDoesNotCertifyQuality(t *testing.T) {
	for _, raw := range []string{"Feature.DVDRip.1080p.x264", "Feature.HDRip.1080p.x264"} {
		got := media.RecognizeRelease(raw)
		if got.Title != "Feature" || got.Quality.Source != "" || len(got.UnitKeys) != 0 || !got.RequiresReview {
			t.Fatalf("cleanup promoted incomplete evidence: %+v", got)
		}
		policy := quality.DefaultPolicy()
		rules := quality.PolicyRules{ResolutionOrder: policy.ResolutionOrder, SourceOrder: policy.SourceOrder}
		decision := quality.EvaluateCandidate(rules, raw, got.Quality)
		owned := quality.AssessOwnership(rules, []quality.FileEvidence{{Title: raw, Profile: got.Quality, Verified: true}})
		if decision.Passed || decision.RejectedReason != "quality_unknown" || owned.Owned || !owned.Unknown {
			t.Fatalf("unknown rip evidence acquired candidate/ownership authority: %+v %+v", decision, owned)
		}
	}
}

func TestRipTitleBoundaryUsesCompleteTokensAndKeepsWorkYear(t *testing.T) {
	for _, tc := range []struct {
		raw, title string
		year       int
	}{
		{"DVDRipper.2014", "DVDRipper", 2014},
		{"HDRipoff.2014", "HDRipoff", 2014},
		{"DVD.2014", "DVD", 2014},
		{"Feature.2014.DVDRip.x264", "Feature", 2014},
		{"Feature.DVDRip.2014.x264", "Feature", 0},
	} {
		got := media.RecognizeRelease(tc.raw)
		if got.Title != tc.title || got.Year != tc.year {
			t.Fatalf("work prefix/technical boundary mismatch for %q: %+v", tc.raw, got)
		}
		if tc.year == 0 && (!got.RequiresReview || len(got.UnitKeys) != 0) {
			t.Fatalf("technical suffix year certified movie coverage: %+v", got)
		}
	}
}

func TestRipTailYearDoesNotResolveCompetingTitleYears(t *testing.T) {
	for _, raw := range []string{"Feature.2049.DVDRip.2014", "Feature.2014.HDRip.2015"} {
		got := media.RecognizeRelease(raw)
		if got.Year != 0 || got.Confidence != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || got.Reason != "ambiguous_media_year" {
			t.Fatalf("technical clipping selected one of competing years: %+v", got)
		}
	}
	got := media.RecognizeRelease("Feature.2014.DVDRip.2014")
	if got.Year != 2014 || got.Title != "Feature" || got.Quality.Source != "" {
		t.Fatalf("repeated year altered prefix/unknown-quality evidence: %+v", got)
	}
}
