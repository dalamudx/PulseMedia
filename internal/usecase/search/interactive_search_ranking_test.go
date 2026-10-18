package search_test

import (
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

func persistedInteractiveProfile(t *testing.T, p quality.Profile) []byte {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func knownInteractiveProfile() quality.Profile {
	return quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion}
}
func rankedInteractiveRow(id string, seeders int32, raw []byte) ports.InteractiveSearchCandidate {
	return ports.InteractiveSearchCandidate{Candidate: ports.SearchCandidate{ID: uuid.MustParse(id), Seeders: seeders, QualityProfile: raw, Title: "Invented.2160p.Remux.DV"}, QualityScore: 999999}
}

func TestInteractiveSearchRankingGlobalOrderBothDirections(t *testing.T) {
	profile := knownInteractiveProfile()
	low := persistedInteractiveProfile(t, profile)
	profile.Resolution = quality.Res2160p
	high := persistedInteractiveProfile(t, profile)
	a := rankedInteractiveRow("00000000-0000-0000-0000-000000000001", math.MinInt32, high)
	b := rankedInteractiveRow("00000000-0000-0000-0000-000000000002", math.MaxInt32, low)
	c := rankedInteractiveRow("00000000-0000-0000-0000-000000000003", math.MaxInt32, low)
	d := rankedInteractiveRow("00000000-0000-0000-0000-000000000004", -1, low)
	e := rankedInteractiveRow("00000000-0000-0000-0000-000000000005", 0, low)
	input := []ports.InteractiveSearchCandidate{c, d, a, e, b}
	want := []uuid.UUID{a.Candidate.ID, b.Candidate.ID, c.Candidate.ID, e.Candidate.ID, d.Candidate.ID}
	reversed := slices.Clone(input)
	slices.Reverse(reversed)
	for _, rows := range [][]ports.InteractiveSearchCandidate{input, reversed} {
		out, err := search.RankInteractiveSearchCandidates(rows, quality.DefaultPolicy())
		if err != nil {
			t.Fatal(err)
		}
		got := make([]uuid.UUID, len(out))
		for i, row := range out {
			got[i] = row.Candidate.ID
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wrong global ordering: %v", got)
		}
		if out[0].QualityScore != 430 || out[1].QualityScore != 330 {
			t.Fatal("quality first score", out)
		}
		for _, row := range rows {
			if row.QualityScore != 999999 {
				t.Fatal("rank mutated input")
			}
		}
	}
	custom := quality.DefaultPolicy()
	custom.ID = "other"
	custom.Revision = 17
	custom.ResolutionOrder[quality.Res1080p] = 1000
	out, err := search.RankInteractiveSearchCandidates(input, custom)
	if err != nil || out[0].Candidate.ID != b.Candidate.ID || out[0].QualityScore != 1030 {
		t.Fatal("frozen policy ignored", out, err)
	}
}

func TestInteractiveSearchRankingPersistedEvidenceOnly(t *testing.T) {
	base := knownInteractiveProfile()
	withHDR := base
	withHDR.HDR = quality.HDRDolbyVision
	withHDR.HDRParserVersion = quality.HDRTokenParserVersion
	oldHDR := withHDR
	oldHDR.HDRParserVersion = "legacy"
	oldSource := withHDR
	oldSource.SourceParserVersion = "legacy"
	oldResolution := withHDR
	oldResolution.ResolutionParserVersion = "legacy"
	unknownResolution := base
	unknownResolution.Resolution = "8k"
	unknownSource := base
	unknownSource.Source = "unknown"
	cases := []struct {
		name string
		raw  []byte
		want int
	}{
		{"current", persistedInteractiveProfile(t, base), 330},
		{"HDR", persistedInteractiveProfile(t, withHDR), 345},
		{"old HDR", persistedInteractiveProfile(t, oldHDR), 330},
		{"old source hold", persistedInteractiveProfile(t, oldSource), 0},
		{"old resolution hold", persistedInteractiveProfile(t, oldResolution), 0},
		{"unknown resolution", persistedInteractiveProfile(t, unknownResolution), 0},
		{"unknown source", persistedInteractiveProfile(t, unknownSource), 0},
		{"empty object", []byte(`{}`), 0},
		{"null", []byte(`null`), 0},
		{"absent", nil, 0},
		{"malformed", []byte(`{"resolution":"2160p"`), 0},
		{"wrong type", []byte(`{"resolution":42}`), 0},
		{"array", []byte(`[]`), 0},
		{"oversized", append(persistedInteractiveProfile(t, base), []byte(strings.Repeat(" ", 16384))...), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := rankedInteractiveRow("00000000-0000-0000-0000-000000000001", 999, tc.raw)
			before := slices.Clone(row.Candidate.QualityProfile)
			out, err := search.RankInteractiveSearchCandidates([]ports.InteractiveSearchCandidate{row}, quality.DefaultPolicy())
			if err != nil || len(out) != 1 || out[0].QualityScore != tc.want {
				t.Fatalf("persisted profile score %v %v", out, err)
			}
			if !reflect.DeepEqual(row.Candidate.QualityProfile, before) {
				t.Fatal("profile rewritten")
			}
		})
	}
	policy := quality.DefaultPolicy()
	policy.PreferHDR = false
	out, err := search.RankInteractiveSearchCandidates([]ports.InteractiveSearchCandidate{{Candidate: ports.SearchCandidate{QualityProfile: persistedInteractiveProfile(t, withHDR)}}}, policy)
	if err != nil || out[0].QualityScore != 330 {
		t.Fatal("HDR policy ignored", out, err)
	}
}

func TestInteractiveSearchRankingRejectsCorruptPolicy(t *testing.T) {
	cases := []func(*quality.Policy){
		func(p *quality.Policy) { p.ID = "" }, func(p *quality.Policy) { p.ID = "bad\n" }, func(p *quality.Policy) { p.ID = strings.Repeat("x", 129) },
		func(p *quality.Policy) { p.Revision = 0 }, func(p *quality.Policy) { p.Revision = -1 },
		func(p *quality.Policy) { p.ResolutionOrder = nil }, func(p *quality.Policy) { p.SourceOrder = nil },
		func(p *quality.Policy) { p.ResolutionOrder[quality.Res1080p] = -1 }, func(p *quality.Policy) { p.SourceOrder[quality.SourceRemux] = -1 },
		func(p *quality.Policy) { p.ResolutionOrder[quality.Res1080p] = math.MaxInt }, func(p *quality.Policy) { p.SourceOrder[quality.SourceRemux] = 100001 },
		func(p *quality.Policy) { p.ResolutionOrder["unknown"] = 1 }, func(p *quality.Policy) { p.SourceOrder["unknown"] = 1 },
	}
	for i, mutate := range cases {
		p := quality.DefaultPolicy()
		mutate(&p)
		out, err := search.RankInteractiveSearchCandidates(nil, p)
		if err != ports.ErrRejected || out != nil {
			t.Fatalf("corrupt policy %d accepted: %v %v", i, out, err)
		}
	}
	policy := quality.DefaultPolicy()
	policy.ResolutionOrder = map[quality.Resolution]int{quality.Res1080p: 0}
	policy.SourceOrder = map[quality.Source]int{quality.SourceWEBDL: 0}
	policy.PreferHDR = false
	out, err := search.RankInteractiveSearchCandidates([]ports.InteractiveSearchCandidate{{Candidate: ports.SearchCandidate{QualityProfile: persistedInteractiveProfile(t, knownInteractiveProfile())}}}, policy)
	if err != nil || out[0].QualityScore != 0 {
		t.Fatal("valid zero-score policy rejected", err)
	}
	empty, err := search.RankInteractiveSearchCandidates(nil, quality.DefaultPolicy())
	if err != nil || len(empty) != 0 {
		t.Fatal(err)
	}
}
