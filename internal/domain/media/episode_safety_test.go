package media_test

import (
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"reflect"
	"strings"
	"testing"
)

func TestExplicitDashedListNeverExpandsUnlistedEpisodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		keys   []string
		review bool
		reason string
	}{
		{"Show.S01E01-E03-E05.mkv", []string{"S01E01", "S01E03", "S01E05"}, false, "explicit_episode_list"},
		{"Show.S01E01-E02-E03.mp4", []string{"S01E01", "S01E02", "S01E03"}, false, "explicit_episode_list"},
		{"Show.S01E01-E02-E00.mkv", nil, true, "invalid_episode_coverage"},
		{"Show.S01E01-E02-E02.mkv", nil, true, "invalid_episode_coverage"},
		{"Show.S01E01-E02-E03.Sample.mkv", nil, true, "partial_episode_content"},
		{"Show.S01E01-E02-E03.S02E04.mkv", nil, true, "conflicting_episode_coverage"},
		{"Show.S01E01-E02-03.mkv", nil, true, "episode_coverage_unresolved"},
		{"Show.S01E01-03-05.mkv", nil, true, "episode_coverage_unresolved"},
	} {
		got := media.RecognizeRelease(tc.name)
		if !reflect.DeepEqual(got.UnitKeys, tc.keys) || got.RequiresReview != tc.review || got.Reason != tc.reason {
			t.Fatalf("explicit list contract: %+v want %+v", got, tc)
		}
	}
	for _, name := range []string{"Show.S01E01-E02-E03.mkv", "Show.S01E01-E02.mkv"} {
		if got := media.RecognizeRelease(name); got.Quality.Group != "" {
			t.Fatalf("coverage token became release group: %+v", got)
		}
	}
}

func TestExplicitDashedListBoundedAtTwentyUnits(t *testing.T) {
	for _, count := range []int{20, 21} {
		eps := make([]string, count)
		for i := range eps {
			eps[i] = fmt.Sprintf("E%02d", i+1)
		}
		got := media.RecognizeRelease("Show.S01" + strings.Join(eps, "-") + ".mkv")
		if count == 20 {
			if got.RequiresReview || len(got.UnitKeys) != 20 || got.UnitKeys[0] != "S01E01" || got.UnitKeys[19] != "S01E20" {
				t.Fatalf("bounded explicit list lost: %+v", got)
			}
		} else if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || got.Reason != "invalid_episode_coverage" {
			t.Fatalf("unbounded list authority: %+v", got)
		}
	}
}

func TestEpisodeCoverageNeverExpandsGapsOrGuessesAbsoluteSeason(t *testing.T) {
	for _, c := range []struct {
		name   string
		keys   []string
		review bool
		reason string
	}{{"Show.S01E01E03.1080p", []string{"S01E01", "S01E03"}, false, "explicit_episode_list"}, {"Show.S01E01-E03.1080p", []string{"S01E01", "S01E02", "S01E03"}, false, "multi_episode_range"}, {"Show.S01E03-E01.mkv", nil, true, "invalid_episode_range"}, {"Show.S01E01-E99.mkv", nil, true, "invalid_episode_range"}, {"Show.S01E00.mkv", nil, true, "invalid_episode_coverage"}, {"Show.S01E01.S02E02.mkv", nil, true, "conflicting_episode_coverage"}, {"[Subs] Show - 125 [1080p].mkv", nil, true, "absolute_mapping_required"}, {"Show.S01.1080p", nil, true, "episode_coverage_unresolved"}, {"S01E01.mkv", nil, true, "missing_media_title"}, {"Show.S01E01.Sample.mkv", nil, true, "partial_episode_content"}, {"Show.S01E04.5.mkv", nil, true, "episode_coverage_unresolved"}, {"Show.S01E04-.mkv", nil, true, "episode_coverage_unresolved"}, {"Show.1x23-24.mkv", []string{"S01E23", "S01E24"}, false, "alt_multi_episode_range"}, {"Show.1x01-03-05.mkv", nil, true, "episode_coverage_unresolved"}, {"Show.S01E01-03-05.mkv", nil, true, "episode_coverage_unresolved"}, {"Show.S01E01_E03.mkv", nil, true, "episode_coverage_unresolved"}, {"Show.1x03-01.mkv", nil, true, "invalid_episode_range"}, {"Show.S01E02.Season.3.mkv", nil, true, "conflicting_episode_coverage"}, {"Show.S01E02.E09.mkv", nil, true, "conflicting_episode_coverage"}, {"Show.Seasons.1-3.2010.mkv", nil, true, "episode_coverage_unresolved"}, {"Show_S01E01_2010.mkv", nil, true, "episode_coverage_unresolved"}, {"Show.S123E01.2010.mkv", nil, true, "episode_coverage_unresolved"}} {
		t.Run(c.name, func(t *testing.T) {
			r := media.RecognizeRelease(c.name)
			if !reflect.DeepEqual(r.UnitKeys, c.keys) || r.RequiresReview != c.review || r.Reason != c.reason || r.ParserVersion != media.EpisodeParserVersion {
				t.Fatalf("wrong coverage: %+v", r)
			}
		})
	}
}
