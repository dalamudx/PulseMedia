package acquisition_test

import (
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

var _ ports.AutomaticTVSelector = acquisition.SelectAutomaticTVEpisode

func automaticTVJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func automaticTVProof(row *ports.AutomaticTVRoundCandidate) {
	c := row.Candidate
	hash := ""
	if c.InfoHash != nil {
		hash = *c.InfoHash
	}
	row.QuerySourceStamp = quality.AcquisitionSourceStamp(c.Title, hash, c.LinkEncrypted, c.QualityProfile)
}

func automaticTVMaterial(t *testing.T) ports.AutomaticTVSelectionMaterial {
	t.Helper()
	policy := quality.DefaultPolicy()
	id := func(n byte) uuid.UUID { return uuid.UUID{15: n} }
	observed := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	input := ports.AutomaticTVSelectionMaterial{
		Snapshot: ports.AutomaticTVRoundSnapshot{
			SchemaVersion: "automatic-tv-round-v1", IdentityRuleVersion: media.AutomaticTVIdentityRuleVersion, ParserVersion: media.EpisodeParserVersion,
			SubscriptionID: id(1), OwnerID: id(2), WantedID: id(3), IntentVersion: 4,
			PolicyID: "frozen-tv", PolicyRevision: 7,
			Rules:   automaticTVJSON(t, quality.PolicyRules{CutoffScore: 400, PreferHDR: policy.PreferHDR, ResolutionOrder: policy.ResolutionOrder, SourceOrder: policy.SourceOrder}),
			MediaID: id(4), MediaVersion: 2, TMDBID: 100, Title: "Fixture", OriginalTitle: "Original Fixture", Year: 2020,
			UnitID: id(5), UnitKey: "S01E03", UnitVersion: 3, EpisodeID: 1003, Season: 1, Episode: 3,
			AirDate: observed.AddDate(0, 0, -1), ObservedAt: observed, MappingMode: "standard", MappingRevision: 2,
			RootID: id(6), RootGeneration: 3, RootHealthRevision: 0, ClientID: id(7), ClientStamp: strings.Repeat("a", 64),
			IndexerVersions: map[string]int64{id(8).String(): 5},
		},
		Candidates: []ports.AutomaticTVRoundCandidate{{
			Candidate: ports.SearchCandidate{
				ID: id(9), IndexerID: id(8), Title: "Fixture.2020.S01E03.1080p.WEB-DL.mkv", SourceConfigVersion: 5,
				LinkEncrypted: []byte{1, 2, 3}, SizeBytes: 1024, Seeders: 10,
				QualityProfile: automaticTVJSON(t, quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion,
					Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion}),
			},
			QueryID: id(10), QueryParametersHash: strings.Repeat("b", 64),
		}},
	}
	automaticTVProof(&input.Candidates[0])
	return input
}

func assertAutomaticTVClosed(t *testing.T, got ports.AutomaticTVSelectionDecision, reason string) {
	t.Helper()
	if got.CandidateID != nil || got.Score != -1 || got.Reason != reason ||
		got.IdentityRuleVersion != media.AutomaticTVIdentityRuleVersion || got.ParserVersion != media.EpisodeParserVersion {
		t.Fatalf("unexpected closed selection: %+v", got)
	}
}

func assertAutomaticTVSelected(t *testing.T, input ports.AutomaticTVSelectionMaterial, id uuid.UUID, score int) {
	t.Helper()
	got := acquisition.SelectAutomaticTVEpisode(input)
	if got.CandidateID == nil || *got.CandidateID != id || got.Score != score || got.Reason != "selected" ||
		got.IdentityRuleVersion != media.AutomaticTVIdentityRuleVersion || got.ParserVersion != media.EpisodeParserVersion {
		t.Fatalf("selection = %+v; want %s score %d", got, id, score)
	}
}

func TestAutomaticTVSelectionStrictTargetAndPersistedQuality(t *testing.T) {
	input := automaticTVMaterial(t)
	assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, 330)
	input.Snapshot.MappingRevision = 0 // No explicit mapping: standard catalog numbering.
	assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, 330)
	// Cutoff is an ownership/upgrade threshold, not an acquisition minimum.
	for _, title := range []string{"Original.Fixture.2020.S01E03.1080p.WEB-DL.mkv", "Fixture.2020.1x03.1080p.WEB-DL.mkv", "Fixture.2020.S01E03.{tmdb-100}.mkv"} {
		input.Candidates[0].Candidate.Title = title
		automaticTVProof(&input.Candidates[0])
		assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, 330)
	}
	input = automaticTVMaterial(t)
	input.Snapshot.AirDate = time.Date(2026, 10, 19, 23, 59, 59, 0, time.UTC)
	assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, 330)
	for _, hash := range []string{strings.Repeat("ab", 20), strings.Repeat("AB", 20)} {
		input.Candidates[0].Candidate.InfoHash = &hash
		automaticTVProof(&input.Candidates[0])
		assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, 330)
	}
}

func TestAutomaticTVSelectionRejectsUnprovenTarget(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ports.AutomaticTVRoundSnapshot)
	}{
		{"schema", func(s *ports.AutomaticTVRoundSnapshot) { s.SchemaVersion = "old" }},
		{"rule", func(s *ports.AutomaticTVRoundSnapshot) { s.IdentityRuleVersion = "old" }},
		{"parser", func(s *ports.AutomaticTVRoundSnapshot) { s.ParserVersion = "old" }},
		{"subscription", func(s *ports.AutomaticTVRoundSnapshot) { s.SubscriptionID = uuid.Nil }},
		{"owner", func(s *ports.AutomaticTVRoundSnapshot) { s.OwnerID = uuid.Nil }},
		{"wanted", func(s *ports.AutomaticTVRoundSnapshot) { s.WantedID = uuid.Nil }},
		{"media", func(s *ports.AutomaticTVRoundSnapshot) { s.MediaID = uuid.Nil }},
		{"unit", func(s *ports.AutomaticTVRoundSnapshot) { s.UnitID = uuid.Nil }},
		{"root", func(s *ports.AutomaticTVRoundSnapshot) { s.RootID = uuid.Nil }},
		{"client", func(s *ports.AutomaticTVRoundSnapshot) { s.ClientID = uuid.Nil }},
		{"intent version", func(s *ports.AutomaticTVRoundSnapshot) { s.IntentVersion = 0 }},
		{"media version", func(s *ports.AutomaticTVRoundSnapshot) { s.MediaVersion = 0 }},
		{"unit version", func(s *ports.AutomaticTVRoundSnapshot) { s.UnitVersion = 0 }},
		{"policy revision", func(s *ports.AutomaticTVRoundSnapshot) { s.PolicyRevision = 0 }},
		{"negative mapping revision", func(s *ports.AutomaticTVRoundSnapshot) { s.MappingRevision = -1 }},
		{"root generation", func(s *ports.AutomaticTVRoundSnapshot) { s.RootGeneration = 0 }},
		{"health revision", func(s *ports.AutomaticTVRoundSnapshot) { s.RootHealthRevision = -1 }},
		{"client stamp absent", func(s *ports.AutomaticTVRoundSnapshot) { s.ClientStamp = "" }},
		{"client stamp malformed", func(s *ports.AutomaticTVRoundSnapshot) { s.ClientStamp = strings.Repeat("A", 64) }},
		{"tmdb", func(s *ports.AutomaticTVRoundSnapshot) { s.TMDBID = 0 }},
		{"year", func(s *ports.AutomaticTVRoundSnapshot) { s.Year = 0 }},
		{"year overflow", func(s *ports.AutomaticTVRoundSnapshot) { s.Year = 10000 }},
		{"catalog missing", func(s *ports.AutomaticTVRoundSnapshot) { s.Title, s.OriginalTitle = "", "" }},
		{"absolute mapping", func(s *ports.AutomaticTVRoundSnapshot) { s.MappingMode = "absolute" }},
		{"manual mapping", func(s *ports.AutomaticTVRoundSnapshot) { s.MappingMode = "manual" }},
		{"special season", func(s *ports.AutomaticTVRoundSnapshot) { s.Season = 0; s.UnitKey = "S00E03" }},
		{"season overflow", func(s *ports.AutomaticTVRoundSnapshot) { s.Season = 1000 }},
		{"episode zero", func(s *ports.AutomaticTVRoundSnapshot) { s.Episode = 0 }},
		{"episode overflow", func(s *ports.AutomaticTVRoundSnapshot) { s.Episode = 10000 }},
		{"episode id", func(s *ports.AutomaticTVRoundSnapshot) { s.EpisodeID = 0 }},
		{"noncanonical key", func(s *ports.AutomaticTVRoundSnapshot) { s.UnitKey = "S1E3" }},
		{"wrong key", func(s *ports.AutomaticTVRoundSnapshot) { s.UnitKey = "S01E02" }},
		{"no observation", func(s *ports.AutomaticTVRoundSnapshot) { s.ObservedAt = time.Time{} }},
		{"unknown air date", func(s *ports.AutomaticTVRoundSnapshot) { s.AirDate = time.Time{} }},
		{"future air date", func(s *ports.AutomaticTVRoundSnapshot) { s.AirDate = s.ObservedAt.AddDate(0, 0, 1) }},
		{"policy id", func(s *ports.AutomaticTVRoundSnapshot) { s.PolicyID = "" }},
		{"policy whitespace", func(s *ports.AutomaticTVRoundSnapshot) { s.PolicyID = " frozen " }},
		{"policy control", func(s *ports.AutomaticTVRoundSnapshot) { s.PolicyID = "bad\npolicy" }},
		{"policy id bound", func(s *ports.AutomaticTVRoundSnapshot) { s.PolicyID = strings.Repeat("x", 129) }},
		{"rules absent", func(s *ports.AutomaticTVRoundSnapshot) { s.Rules = nil }},
		{"rules null", func(s *ports.AutomaticTVRoundSnapshot) { s.Rules = []byte(`null`) }},
		{"rules malformed", func(s *ports.AutomaticTVRoundSnapshot) { s.Rules = []byte(`{"`) }},
		{"rules incomplete", func(s *ports.AutomaticTVRoundSnapshot) { s.Rules = []byte(`{}`) }},
		{"rules unknown field", func(s *ports.AutomaticTVRoundSnapshot) {
			s.Rules = append(s.Rules[:len(s.Rules)-1], []byte(`,"unknown":true}`)...)
		}},
		{"rules trailing value", func(s *ports.AutomaticTVRoundSnapshot) { s.Rules = append(s.Rules, []byte(` {}`)...) }},
		{"rules bound", func(s *ports.AutomaticTVRoundSnapshot) {
			s.Rules = append(s.Rules, []byte(strings.Repeat(" ", 65536))...)
		}},
		{"indexers absent", func(s *ports.AutomaticTVRoundSnapshot) { s.IndexerVersions = nil }},
		{"indexer key", func(s *ports.AutomaticTVRoundSnapshot) { s.IndexerVersions["bad"] = 1 }},
		{"indexer zero", func(s *ports.AutomaticTVRoundSnapshot) { s.IndexerVersions[uuid.Nil.String()] = 1 }},
		{"indexer version", func(s *ports.AutomaticTVRoundSnapshot) {
			for k := range s.IndexerVersions {
				s.IndexerVersions[k] = 0
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := automaticTVMaterial(t)
			tc.change(&input.Snapshot)
			assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), "unproven_target")
		})
	}
	assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(ports.AutomaticTVSelectionMaterial{}), "unproven_target")
}

func TestAutomaticTVSelectionIdentityAndSparseCoverage(t *testing.T) {
	for _, title := range []string{
		"Other.2020.S01E03.1080p.WEB-DL.mkv", "Fixture.2021.S01E03.mkv", "Fixture.S01E03.{tmdb-100}.mkv",
		"Fixture.2020.S01E03.{tmdb-999}.mkv", "Fixture.2020.S01E03.{tmdb-100}.{tmdb-999}.mkv",
		"Fixture.2020.S01E02.mkv", "Fixture.2020.S02E03.mkv", "Fixture.2020.S00E03.mkv",
		"[Team] Fixture.2020 - 03 [1080p].mkv", "Fixture.2020.S01E01-E03.mkv", "Fixture.2020.S01E03-E03.mkv",
		"Fixture.2020.S01E01E03.mkv", "Fixture.2020.S01.COMPLETE.mkv", "Fixture.2020.S01E03.S01E03.mkv",
		"Fixture.2020.1080p.Other.S01E03.mkv", "Fixture.2020.S01E03.tt1234567.mkv",
	} {
		t.Run(title, func(t *testing.T) {
			input := automaticTVMaterial(t)
			input.Candidates[0].Candidate.Title = title
			// Cached match suggestions cannot establish identity or coverage.
			input.Candidates[0].Candidate.MatchedMediaID = &input.Snapshot.MediaID
			input.Candidates[0].Candidate.MatchedUnitID = &input.Snapshot.UnitID
			automaticTVProof(&input.Candidates[0])
			assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), "no_qualified_candidate")
		})
	}
	input := automaticTVMaterial(t)
	input.Candidates = nil
	assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), "no_qualified_candidate")
}

func TestAutomaticTVSelectionSourceProofRequired(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ports.AutomaticTVRoundCandidate)
	}{
		{"candidate id", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.ID = uuid.Nil }},
		{"indexer id", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.IndexerID = uuid.Nil }},
		{"other indexer", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.IndexerID = uuid.UUID{15: 99} }},
		{"config changed", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.SourceConfigVersion++ }},
		{"config absent", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.SourceConfigVersion = 0 }},
		{"query id", func(r *ports.AutomaticTVRoundCandidate) { r.QueryID = uuid.Nil }},
		{"query hash absent", func(r *ports.AutomaticTVRoundCandidate) { r.QueryParametersHash = "" }},
		{"query hash uppercase", func(r *ports.AutomaticTVRoundCandidate) { r.QueryParametersHash = strings.Repeat("A", 64) }},
		{"query hash nonhex", func(r *ports.AutomaticTVRoundCandidate) { r.QueryParametersHash = strings.Repeat("z", 64) }},
		{"query hash short", func(r *ports.AutomaticTVRoundCandidate) { r.QueryParametersHash = strings.Repeat("a", 63) }},
		{"source stamp absent", func(r *ports.AutomaticTVRoundCandidate) { r.QuerySourceStamp = "" }},
		{"source stamp mismatch", func(r *ports.AutomaticTVRoundCandidate) { r.QuerySourceStamp = strings.Repeat("a", 64) }},
		{"title changed", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.Title += ".changed" }},
		{"link changed", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.LinkEncrypted[0]++ }},
		{"profile changed", func(r *ports.AutomaticTVRoundCandidate) {
			r.Candidate.QualityProfile = append(r.Candidate.QualityProfile, ' ')
		}},
		{"hash changed", func(r *ports.AutomaticTVRoundCandidate) {
			hash := strings.Repeat("a", 40)
			r.Candidate.InfoHash = &hash
		}},
		{"size zero", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.SizeBytes = 0 }},
		{"size negative", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.SizeBytes = -1 }},
		{"no cipher", func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.LinkEncrypted = nil; automaticTVProof(r) }},
		{"invalid hash", func(r *ports.AutomaticTVRoundCandidate) {
			hash := strings.Repeat("z", 40)
			r.Candidate.InfoHash = &hash
			automaticTVProof(r)
		}},
		{"short hash", func(r *ports.AutomaticTVRoundCandidate) {
			hash := "abc"
			r.Candidate.InfoHash = &hash
			automaticTVProof(r)
		}},
		{"empty hash", func(r *ports.AutomaticTVRoundCandidate) {
			hash := ""
			r.Candidate.InfoHash = &hash
			automaticTVProof(r)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := automaticTVMaterial(t)
			tc.change(&input.Candidates[0])
			assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), "no_qualified_candidate")
		})
	}
	input := automaticTVMaterial(t)
	input.Snapshot.IndexerVersions[input.Candidates[0].Candidate.IndexerID.String()]++
	assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), "no_qualified_candidate")
}

func TestAutomaticTVSelectionQualityEvidence(t *testing.T) {
	base := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion,
		Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion}
	for _, field := range []string{"resolution", "resolution parser", "source", "source parser"} {
		t.Run(field, func(t *testing.T) {
			p := base
			switch field {
			case "resolution":
				p.Resolution = "unknown"
			case "resolution parser":
				p.ResolutionParserVersion = "legacy"
			case "source":
				p.Source = "unknown"
			case "source parser":
				p.SourceParserVersion = "legacy"
			}
			input := automaticTVMaterial(t)
			input.Candidates[0].Candidate.QualityProfile = automaticTVJSON(t, p)
			automaticTVProof(&input.Candidates[0])
			assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), "no_qualified_candidate")
		})
	}
	for i, raw := range [][]byte{nil, []byte(`null`), []byte(`{}`), []byte(`[]`), []byte(`{"resolution":42}`), []byte(`{"resolution":"2160p"`), []byte(`{"hdr":"DV"}`), append(automaticTVJSON(t, base), []byte(strings.Repeat(" ", 16384))...)} {
		input := automaticTVMaterial(t)
		input.Candidates[0].Candidate.QualityProfile = raw
		automaticTVProof(&input.Candidates[0])
		got := acquisition.SelectAutomaticTVEpisode(input)
		if got.CandidateID != nil {
			t.Fatalf("unknown profile %d selected: %+v", i, got)
		}
		assertAutomaticTVClosed(t, got, "no_qualified_candidate")
	}
	// Evidence is persisted, never invented from a quality-looking title.
	input := automaticTVMaterial(t)
	input.Candidates[0].Candidate.Title = "Fixture.2020.S01E03.2160p.Remux.DV.mkv"
	automaticTVProof(&input.Candidates[0])
	assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, 330)
	for _, version := range []string{quality.HDRTokenParserVersion, "legacy"} {
		p := base
		p.HDR, p.HDRParserVersion = quality.HDRDolbyVision, version
		input.Candidates[0].Candidate.QualityProfile = automaticTVJSON(t, p)
		automaticTVProof(&input.Candidates[0])
		score := 330
		if version == quality.HDRTokenParserVersion {
			score = 345
		}
		assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, score)
	}
}

func TestAutomaticTVSelectionFrozenPolicy(t *testing.T) {
	cases := []struct {
		name   string
		change func(*quality.PolicyRules)
		reason string
	}{
		{"resolution minimum", func(r *quality.PolicyRules) { delete(r.ResolutionOrder, quality.Res1080p) }, "no_qualified_candidate"},
		{"source allowed", func(r *quality.PolicyRules) { delete(r.SourceOrder, quality.SourceWEBDL) }, "no_qualified_candidate"},
		{"reject title", func(r *quality.PolicyRules) { r.RejectTerms = []string{"FIXTURE"} }, "no_qualified_candidate"},
		{"reject raw group", func(r *quality.PolicyRules) { r.RejectGroups = []string{"bad"} }, "no_qualified_candidate"},
		{"negative weight", func(r *quality.PolicyRules) { r.ResolutionOrder[quality.Res1080p] = -1 }, "unproven_target"},
		{"weight bound", func(r *quality.PolicyRules) { r.SourceOrder[quality.SourceWEBDL] = 100001 }, "unproven_target"},
		{"unknown resolution", func(r *quality.PolicyRules) { r.ResolutionOrder["8k"] = 1 }, "unproven_target"},
		{"unknown source", func(r *quality.PolicyRules) { r.SourceOrder["unknown"] = 1 }, "unproven_target"},
		{"cutoff bound", func(r *quality.PolicyRules) { r.CutoffScore = 200016 }, "unproven_target"},
		{"reject list bound", func(r *quality.PolicyRules) { r.RejectTerms = make([]string, 101) }, "unproven_target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := automaticTVMaterial(t)
			input.Candidates[0].Candidate.Title = "Fixture.2020.S01E03.1080p.WEB-DL-BAD.mkv"
			automaticTVProof(&input.Candidates[0])
			var rules quality.PolicyRules
			if err := json.Unmarshal(input.Snapshot.Rules, &rules); err != nil {
				t.Fatal(err)
			}
			tc.change(&rules)
			input.Snapshot.Rules = automaticTVJSON(t, rules)
			assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), tc.reason)
		})
	}
	input := automaticTVMaterial(t)
	input.Snapshot.Rules = automaticTVJSON(t, quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 0}, SourceOrder: map[quality.Source]int{quality.SourceWEBDL: 0}})
	assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, 0)
}

func TestAutomaticTVSelectionStableRanking(t *testing.T) {
	input := automaticTVMaterial(t)
	base := input.Candidates[0]
	var profile quality.Profile
	if err := json.Unmarshal(base.Candidate.QualityProfile, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Resolution = quality.Res2160p
	high := base
	high.Candidate.ID = uuid.UUID{15: 90}
	high.Candidate.Seeders = math.MinInt32
	high.Candidate.QualityProfile = automaticTVJSON(t, profile)
	automaticTVProof(&high)
	lowID := base
	lowID.Candidate.ID = uuid.UUID{15: 20}
	lowID.Candidate.Seeders = math.MaxInt32
	highID := lowID
	highID.Candidate.ID = uuid.UUID{15: 21}
	unseeded := lowID
	unseeded.Candidate.ID = uuid.UUID{15: 19}
	unseeded.Candidate.Seeders = -1
	rows := []ports.AutomaticTVRoundCandidate{highID, unseeded, high, lowID}
	var permutations func(int)
	permutations = func(start int) {
		if start == len(rows) {
			input.Candidates = slices.Clone(rows)
			assertAutomaticTVSelected(t, input, high.Candidate.ID, 430)
			input.Candidates = nil
			for _, row := range rows {
				if row.Candidate.ID != high.Candidate.ID {
					input.Candidates = append(input.Candidates, row)
				}
			}
			assertAutomaticTVSelected(t, input, lowID.Candidate.ID, 330)
			return
		}
		for i := start; i < len(rows); i++ {
			rows[start], rows[i] = rows[i], rows[start]
			permutations(start + 1)
			rows[start], rows[i] = rows[i], rows[start]
		}
	}
	permutations(0)
	input.Candidates = rows
	var rules quality.PolicyRules
	if err := json.Unmarshal(input.Snapshot.Rules, &rules); err != nil {
		t.Fatal(err)
	}
	rules.ResolutionOrder[quality.Res1080p] = 1000
	input.Snapshot.Rules = automaticTVJSON(t, rules)
	assertAutomaticTVSelected(t, input, lowID.Candidate.ID, 1030)
}

func TestAutomaticTVSelectionDuplicateProofs(t *testing.T) {
	input := automaticTVMaterial(t)
	row := input.Candidates[0]
	input.Candidates = append(input.Candidates, row)
	assertAutomaticTVSelected(t, input, row.Candidate.ID, 330)
	input.Candidates[1].QueryID = uuid.UUID{15: 30}
	input.Candidates[1].QueryParametersHash = strings.Repeat("c", 64)
	assertAutomaticTVSelected(t, input, row.Candidate.ID, 330)
	for _, change := range []func(*ports.AutomaticTVRoundCandidate){
		func(r *ports.AutomaticTVRoundCandidate) { r.QuerySourceStamp = strings.Repeat("c", 64) },
		func(r *ports.AutomaticTVRoundCandidate) {
			r.Candidate.Title = "Other.2020.S01E03.mkv"
			automaticTVProof(r)
		},
		func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.Seeders++ },
		func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.SizeBytes++ },
		func(r *ports.AutomaticTVRoundCandidate) { r.Candidate.SourceConfigVersion++ },
	} {
		input.Candidates = []ports.AutomaticTVRoundCandidate{row, row}
		change(&input.Candidates[1])
		assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), "no_qualified_candidate")
		slices.Reverse(input.Candidates)
		assertAutomaticTVClosed(t, acquisition.SelectAutomaticTVEpisode(input), "no_qualified_candidate")
	}
}

func TestAutomaticTVSelectionPureAndNoSourceDisclosure(t *testing.T) {
	input := automaticTVMaterial(t)
	secret := "https://source.invalid/file?passkey=private-secret"
	input.Candidates[0].Candidate.Guid = secret
	hash := strings.Repeat("a", 40)
	input.Candidates[0].Candidate.InfoHash = &hash
	published := input.Snapshot.ObservedAt
	input.Candidates[0].Candidate.PublishDate = &published
	input.Candidates[0].Candidate.MatchedMediaID = &input.Snapshot.MediaID
	input.Candidates[0].Candidate.MatchedUnitID = &input.Snapshot.UnitID
	automaticTVProof(&input.Candidates[0])
	before := automaticTVJSON(t, input)
	var copyBefore ports.AutomaticTVSelectionMaterial
	if err := json.Unmarshal(before, &copyBefore); err != nil {
		t.Fatal(err)
	}
	// LinkEncrypted is intentionally omitted by JSON; keep an independent copy.
	copyBefore.Candidates[0].Candidate.LinkEncrypted = slices.Clone(input.Candidates[0].Candidate.LinkEncrypted)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { assertAutomaticTVSelected(t, input, input.Candidates[0].Candidate.ID, 330) })
	}
	wg.Wait()
	if !reflect.DeepEqual(input, copyBefore) {
		t.Fatal("selector mutated input, pointers, slices or maps")
	}
	got := acquisition.SelectAutomaticTVEpisode(input)
	if strings.Contains(string(automaticTVJSON(t, got)), "private-secret") {
		t.Fatal("source disclosed")
	}
	originalID := input.Candidates[0].Candidate.ID
	*got.CandidateID = uuid.Nil
	if input.Candidates[0].Candidate.ID != originalID {
		t.Fatal("result UUID aliases candidate")
	}
	input.Candidates[0].Candidate.Title = secret
	automaticTVProof(&input.Candidates[0])
	got = acquisition.SelectAutomaticTVEpisode(input)
	assertAutomaticTVClosed(t, got, "no_qualified_candidate")
	if strings.Contains(string(automaticTVJSON(t, got)), secret) {
		t.Fatal("bad source disclosed")
	}
}
