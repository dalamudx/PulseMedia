package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestMovieCandidateRuleDecision(t *testing.T) {
	valid := media.CandidateRuleInput{CandidateID: "one", Title: "Movie.2020.1080p.BluRay-GROUP", TargetTitle: "Movie", Year: 2020, SizeBytes: 100, HasReference: true, SourceVersion: 1, Profile: quality.Profile{Resolution: "1080p", Source: "Bluray", SourceParserVersion: quality.SourceTokenParserVersion, Group: "GROUP"}, Rules: quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{"1080p": 0}, SourceOrder: map[quality.Source]int{"Bluray": 0}, CutoffScore: 400}}
	for _, name := range []string{"valid-low-cutoff", "unknown-quality", "reject-term", "reject-group", "other-title", "no-year", "unknown-size", "missing-reference", "old-source", "unsafe-title"} {
		t.Run(name, func(t *testing.T) {
			in := valid
			decision, reason := "hold", ""
			switch name {
			case "valid-low-cutoff":
				decision, reason = "accept", "rules_allowed"
			case "unknown-quality":
				in.Profile = quality.Profile{}
				reason = "quality_unknown"
			case "reject-term":
				in.Rules.RejectTerms = []string{"movie"}
				decision, reason = "reject", "reject_term"
			case "reject-group":
				in.Rules.RejectGroups = []string{"group"}
				decision, reason = "reject", "reject_group"
			case "other-title":
				in.TargetTitle = "Other"
				reason = "match_unproven"
			case "no-year":
				in.Year = 0
				reason = "match_unproven"
			case "unknown-size":
				in.SizeBytes = 0
				reason = "size_unknown"
			case "missing-reference":
				in.HasReference = false
				reason = "reference_missing"
			case "old-source":
				in.SourceVersion = 0
				reason = "source_unproven"
			case "unsafe-title":
				in.Title = "https://site/?passkey=secret"
				reason = "unsafe_title"
			}
			result := media.ExplainMovieCandidate(in)
			if result.Decision != decision || result.Reason != reason {
				t.Fatal(result)
			}
			if name == "valid-low-cutoff" && result.MeetsCutoff {
				t.Fatal("quality allowed wrongly meets upgrade threshold")
			}
			changed := in
			changed.PolicyRevision++
			if media.CandidateInputDigest(in) == media.CandidateInputDigest(changed) {
				t.Fatal("revision missing from cache key")
			}
			if name == "unsafe-title" && media.SafeCandidateTitle(in.Title) == in.Title {
				t.Fatal("unsafe title leaked")
			}
		})
	}
}
