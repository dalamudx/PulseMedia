package media_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestExplicitFourDigitEpisodesRetainExactCoverage(t *testing.T) {
	for _, tc := range []struct {
		token    string
		episodes []int
		reason   string
	}{
		{"S01E1056", []int{1056}, "standard_season_episode"},
		{"S01E9999", []int{9999}, "standard_season_episode"},
		{"S01E1056-E1056", []int{1056}, "multi_episode_range"},
		{"1x1056", []int{1056}, "alt_season_episode"},
		{"S01E1056-E1058", []int{1056, 1057, 1058}, "multi_episode_range"},
		{"1x1056-1058", []int{1056, 1057, 1058}, "alt_multi_episode_range"},
		{"S01E1056E1058", []int{1056, 1058}, "explicit_episode_list"},
		{"S01E1056-E1058-E1060", []int{1056, 1058, 1060}, "explicit_episode_list"},
		{"S01E999E1000", []int{999, 1000}, "explicit_episode_list"},
	} {
		t.Run(tc.token, func(t *testing.T) {
			got := media.RecognizeRelease("Fixture.2024." + tc.token + ".1080p.WEB-DL.x264.mkv")
			wantKeys := make([]string, len(tc.episodes))
			for i, ep := range tc.episodes {
				wantKeys[i] = fmt.Sprintf("S01E%02d", ep)
			}
			if got.Title != "Fixture" || got.Year != 2024 || got.MediaType != media.TypeTV || got.Season != 1 || !reflect.DeepEqual(got.Episodes, tc.episodes) || !reflect.DeepEqual(got.UnitKeys, wantKeys) || got.Confidence != .95 || got.RequiresReview || got.IsAnime || got.AbsoluteEpisode != 0 || got.Reason != tc.reason {
				t.Fatalf("explicit high numbering lost, padded or remapped: %+v; want %v", got, tc.episodes)
			}
		})
	}
}

func TestFourDigitEpisodeBoundariesDoNotInventCoverage(t *testing.T) {
	for _, token := range []string{
		"S01E10000", "1x10000", "S01E9999-E10000", "S01E9999E10000", "S01E1056-E1058-E10000",
		"S01E1056.5", "S01E1056sample", "S01E1056.sample", "S01E1056.first-half", "S01E1056.S02E01",
		"S01E1056.1x1058", "E1056", "S01E1056E1056", "S01E1056-E1040", "S01E1056-E1076",
	} {
		t.Run(token, func(t *testing.T) {
			got := media.RecognizeRelease("Fixture.2024." + token + ".1080p.WEB-DL.x264.mkv")
			if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 {
				t.Fatalf("overlong/partial/conflicting/missing-season coverage certified: %+v", got)
			}
		})
	}
}

func TestExplicitEpisodeListsHaveConsistentTwentyUnitBound(t *testing.T) {
	for _, separator := range []string{"", "-"} {
		makeName := func(count int) string {
			terms := make([]string, count)
			for i := range count {
				terms[i] = fmt.Sprintf("E%d", 1056+i*2)
			}
			return "Fixture.2024.S01" + strings.Join(terms, separator) + ".1080p.WEB-DL.x264.mkv"
		}
		accepted := media.RecognizeRelease(makeName(20))
		if accepted.RequiresReview || accepted.Confidence != .95 || len(accepted.UnitKeys) != 20 || len(accepted.Episodes) != 20 {
			t.Fatalf("bounded exact list lost: %+v", accepted)
		}
		for i, ep := range accepted.Episodes {
			if ep != 1056+i*2 {
				t.Fatalf("gap padded: %+v", accepted)
			}
		}
		held := media.RecognizeRelease(makeName(21))
		if !held.RequiresReview || held.Confidence != 0 || len(held.UnitKeys) != 0 || len(held.Episodes) != 0 {
			t.Fatalf("overlong %q list certified: %+v", separator, held)
		}
	}
}
