package quality_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func selectionPack() quality.PackCandidate {
	return quality.PackCandidate{ID: "pack", Season: 1, TotalSeasonEps: 3, Score: 10, SeasonMetadataKnown: true, CoverageKnown: true, ExpectedEpisodes: []int{1, 3, 7}, CoveredEpisodes: []int{1, 3, 7}}
}

func TestTVSelectionPolicyMatrix(t *testing.T) {
	tests := []struct {
		name         string
		mode         quality.PackMode
		threshold    float64
		missing      []int
		packScore    int
		singles      bool
		kind, reason string
	}{
		{"none_quality", quality.PackModeNone, 0, []int{1, 3, 7}, 9, true, "episodes", "quality_then_stable_id"},
		{"none_not_episode_alias", quality.PackModeNone, 0, []int{1, 3, 7}, 11, true, "pack", "quality_then_stable_id"},
		{"pack_preferred", quality.PackModePreferPack, 1, []int{1, 3, 7}, 0, true, "pack", "pack_preference"},
		{"episodes_preferred", quality.PackModePreferEpisodes, 0, []int{1, 3, 7}, 99, true, "episodes", "episode_preference"},
		{"threshold_equal", quality.PackModePreferPack, 1.0 / 3, []int{3}, 0, true, "pack", "pack_preference"},
		{"threshold_below", quality.PackModePreferPack, 0.5, []int{3}, 0, true, "episodes", "quality_then_stable_id"},
		{"require_complete", quality.PackModeRequirePack, 0, []int{1, 3, 7}, 0, true, "pack", "required_complete_pack"},
		{"episodes_fallback", quality.PackModePreferEpisodes, 0, []int{1, 3, 7}, 0, false, "pack", "only_eligible_alternative"},
		{"no_missing", quality.PackModeRequirePack, 0, nil, 0, true, "hold", "no_missing_targets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := selectionPack()
			p.Score = tt.packScore
			singles := []quality.EpisodeSelectionCandidate{}
			if tt.singles {
				for _, ep := range tt.missing {
					singles = append(singles, quality.EpisodeSelectionCandidate{ID: string(rune('a' + ep)), Episode: ep, Score: 10})
				}
			}
			got, err := quality.ChooseTVSelection(quality.PackPolicy{Mode: tt.mode, MinMissingRatio: tt.threshold}, p.ExpectedEpisodes, tt.missing, []quality.PackCandidate{p}, singles)
			if err != nil || got.Kind != tt.kind || got.Reason != tt.reason {
				t.Fatalf("got %+v err=%v", got, err)
			}
		})
	}
}

func TestTVSelectionRequirePackNeverFallsBack(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*quality.PackCandidate)
	}{
		{"missing_middle", func(p *quality.PackCandidate) { p.CoveredEpisodes = []int{1, 7} }},
		{"endpoints_only", func(p *quality.PackCandidate) { p.CoveredEpisodes = nil; p.StartEp = 1; p.EndEp = 7 }},
		{"unknown", func(p *quality.PackCandidate) { p.CoverageKnown = false }},
		{"unknown_files", func(p *quality.PackCandidate) { p.HasUnknownFiles = true }},
		{"unknown_metadata", func(p *quality.PackCandidate) { p.SeasonMetadataKnown = false }},
		{"special", func(p *quality.PackCandidate) { p.Season = 0 }},
		{"duplicate", func(p *quality.PackCandidate) { p.CoveredEpisodes = []int{1, 3, 3} }},
		{"extra", func(p *quality.PackCandidate) { p.CoveredEpisodes = []int{1, 3, 7, 8} }},
		{"different_target_set", func(p *quality.PackCandidate) {
			p.ExpectedEpisodes = []int{1, 2, 7}
			p.CoveredEpisodes = []int{1, 2, 7}
		}},
	}
	singles := []quality.EpisodeSelectionCandidate{{ID: "a", Episode: 1, Score: 10}, {ID: "b", Episode: 3, Score: 10}, {ID: "c", Episode: 7, Score: 10}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := selectionPack()
			tt.mutate(&p)
			for _, mode := range []quality.PackMode{quality.PackModeRequirePack, quality.PackModePreferPack} {
				got, err := quality.ChooseTVSelection(quality.PackPolicy{Mode: mode}, []int{1, 3, 7}, []int{1, 3, 7}, []quality.PackCandidate{p}, singles)
				want := "episodes"
				if mode == quality.PackModeRequirePack {
					want = "hold"
				}
				if err != nil || got.Kind != want {
					t.Fatalf("%s got %+v err=%v", mode, got, err)
				}
			}
		})
	}
}

func TestTVSelectionStableTiesAndWeakestQuality(t *testing.T) {
	p := selectionPack()
	p.ID = "z"
	p.Score = 10
	p2 := p
	p2.ID = "y"
	singles := []quality.EpisodeSelectionCandidate{{ID: "c", Episode: 7, Score: 90}, {ID: "b", Episode: 3, Score: 90}, {ID: "a2", Episode: 1, Score: 10}, {ID: "a1", Episode: 1, Score: 10}}
	want := []string{"a1", "b", "c"}
	for i := 0; i < 8; i++ {
		got, err := quality.ChooseTVSelection(quality.PackPolicy{Mode: quality.PackModeNone}, []int{1, 3, 7}, []int{7, 1, 3}, []quality.PackCandidate{p, p2}, singles)
		if err != nil || got.Kind != "episodes" || got.Score != 10 || !reflect.DeepEqual(got.CandidateIDs, want) {
			t.Fatalf("got %+v %v", got, err)
		}
		singles = append(singles[1:], singles[0])
	}
	p.Score = 11
	got, err := quality.ChooseTVSelection(quality.PackPolicy{Mode: quality.PackModeNone}, []int{1, 3, 7}, []int{1, 3, 7}, []quality.PackCandidate{p}, singles)
	if err != nil || got.Kind != "pack" {
		t.Fatalf("sum must not beat pack: %+v %v", got, err)
	}
	got, err = quality.ChooseTVSelection(quality.PackPolicy{Mode: quality.PackModePreferPack}, []int{1, 3, 7}, []int{1, 3, 7}, []quality.PackCandidate{p2, p2WithID(p2, "x")}, nil)
	if err != nil || !reflect.DeepEqual(got.CandidateIDs, []string{"x"}) {
		t.Fatalf("pack tie %+v %v", got, err)
	}
}
func p2WithID(p quality.PackCandidate, id string) quality.PackCandidate { p.ID = id; return p }

func TestTVSelectionInvalidInputs(t *testing.T) {
	for _, mode := range []quality.PackMode{"", "auto", "NONE"} {
		t.Run("mode_"+string(mode), func(t *testing.T) {
			_, err := quality.ChooseTVSelection(quality.PackPolicy{Mode: mode}, []int{1}, []int{1}, nil, nil)
			if err == nil {
				t.Fatal("accepted implicit/unknown mode")
			}
		})
	}
	for i, ratio := range []float64{-1, 1.1, math.NaN(), math.Inf(1)} {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			_, err := quality.ChooseTVSelection(quality.PackPolicy{Mode: quality.PackModeNone, MinMissingRatio: ratio}, []int{1}, []int{1}, nil, nil)
			if err == nil {
				t.Fatal("accepted ratio")
			}
		})
	}
	for _, sets := range []struct {
		name              string
		expected, missing []int
	}{
		{"empty", nil, nil}, {"duplicate", []int{1, 1}, []int{1}}, {"invalid", []int{0}, []int{0}}, {"outside", []int{1}, []int{2}}, {"duplicate_missing", []int{1}, []int{1, 1}},
	} {
		t.Run(sets.name, func(t *testing.T) {
			_, err := quality.ChooseTVSelection(quality.PackPolicy{Mode: quality.PackModeNone}, sets.expected, sets.missing, nil, nil)
			if err == nil {
				t.Fatal("accepted invalid sets")
			}
		})
	}
	t.Run("duplicate_candidate", func(t *testing.T) {
		p := selectionPack()
		_, err := quality.ChooseTVSelection(quality.PackPolicy{Mode: quality.PackModeNone}, p.ExpectedEpisodes, p.ExpectedEpisodes, []quality.PackCandidate{p, p}, nil)
		if err == nil {
			t.Fatal("accepted duplicate candidate")
		}
	})
}

