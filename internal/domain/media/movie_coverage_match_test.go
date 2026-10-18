package media

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestMovieIdentityRejectsEveryContradiction(t *testing.T) {
	ids := map[string]string{"tmdb.movie": "550", "imdb": "tt0137523"}
	for _, raw := range []string{"Fight.Club.1999.1080p.{tmdb-550}.{tmdb-999}", "Fight.Club.1999.1080p.{tmdb-550}.tt9999999", "Fight.Club.2020.1080p.{tmdb-550}"} {
		if MatchesAutomaticMovie(raw, "Fight Club", 1999, ids) {
			t.Fatal("movie identity contradicted by another explicit input", raw)
		}
	}
	if !MatchesAutomaticMovie("Fight.Club.1999.1080p.{tmdb-550}.tt0137523", "Fight Club", 1999, ids) {
		t.Fatal("consistent explicit identities rejected")
	}
}

func TestMovieCandidateCacheIncludesCoverageVersion(t *testing.T) {
	in := CandidateRuleInput{Title: "[Team] Fight Club - 25 [1080p] {tmdb-550}", TargetTitle: "Fight Club", Year: 1999, Identities: map[string]string{"tmdb.movie": "550"}, SourceVersion: 1}
	if got := ExplainMovieCandidate(in); got.Decision != "hold" || got.Reason != "match_unproven" {
		t.Fatal("movie rule accepted episode coverage", got)
	}
	raw, _ := json.Marshal([]any{CandidateDecisionVersion, in})
	old := sha256.Sum256(raw)
	if CandidateInputDigest(in) == hex.EncodeToString(old[:]) {
		t.Fatal("coverage parser absent from rule digest")
	}
	raw, _ = json.Marshal([]any{CandidateDecisionVersion, EpisodeParserVersion, in})
	expected := sha256.Sum256(raw)
	if CandidateInputDigest(in) != hex.EncodeToString(expected[:]) {
		t.Fatal("digest no longer matches complete current rule input")
	}
}

func TestExplicitMovieIDCannotHideTVNumberCoverage(t *testing.T) {
	for _, raw := range []string{"[Team] Fight Club - 25 [1080p] {tmdb-550}", "[Team] Fight Club - 25 [1080p] - 27 {tmdb-550}"} {
		if MatchesAutomaticMovie(raw, "Fight Club", 1999, map[string]string{"tmdb.movie": "550"}) {
			t.Fatal("explicit movie ID hid episode evidence")
		}
	}
	if !MatchesAutomaticMovie("Fight.Club.1999.1080p.{tmdb-550}", "Fight Club", 1999, map[string]string{"tmdb.movie": "550"}) {
		t.Fatal("safe movie no longer matches")
	}
}
