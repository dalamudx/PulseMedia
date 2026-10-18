package media

import (
	"reflect"
	"strings"
	"testing"
)

func TestAtamaFrozenTitleIndependentNames(t *testing.T) {
	for _, tc := range []struct {
		name, title, reason string
		confidence          float64
	}{
		{"[project-gxs] Unseen Observatory v9 [10bit BD 1080p]", "Unseen Observatory", "title_only_without_year", .6},
		{"[SallySubs] Unseen Observatory 2 [BD 1080p FLAC]", "Unseen Observatory 2", "title_only_without_year", .6},
		{"[Arukoru] Unseen Observatory (Alternate Observatory) [1080p x265 10bit BD Dual Audio AAC]", "Unseen Observatory", "title_only_without_year", .6},
		{"[Cleo] Unseen Observatory | Alternate Observatory [Dual Audio 10bit BD1080p][HEVC-x265]", "Unseen Observatory", "title_only_without_year", .6},
		{"[Cleo] .unseen//Observatory [Dual Audio 10bit DVD480p][HEVC-x265]", ".unseen//Observatory", "title_only_without_year", .6},
		{"[DeadFish] Unseen Observatory - Special [BD][1080p][AAC].mp4", "Unseen Observatory", "episode_coverage_unresolved", 0},
		{"Unseen Observatory Volume 7 [BD][x264,720p,AAC][10Bit][DarkHollow]", "Unseen Observatory", "episode_coverage_unresolved", 0},
		{"[inid4c] Unseen Observatory WEB 02-11 (1080p)", "Unseen Observatory", "episode_coverage_unresolved", 0},
		{"[inid4c] Unseen Observatory 16 v7 (1080p)", "Unseen Observatory", "episode_coverage_unresolved", 0},
		{"[Watashi]_Unseen_Observatory_09_[576p][1234ABCD].mkv", "", "episode_coverage_unresolved", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := RecognizeRelease(tc.name)
			m := Recognize(tc.name)
			if r.Title != tc.title || m.Title != tc.title || r.Reason != tc.reason || r.Confidence != tc.confidence || m.Confidence != tc.confidence || !r.RequiresReview || !m.RequiresReview || r.RawName != tc.name || m.RawName != tc.name || r.ParserVersion != EpisodeParserVersion || len(r.UnitKeys) != 0 || len(r.Episodes) != 0 || r.Season != 0 || r.AbsoluteEpisode != 0 || m.Identity != nil {
				t.Fatalf("public lexical contract: %+v / %+v", r, m)
			}
		})
	}
}

func TestAtamaFrozenTitleUnknownSourceAndVersionControls(t *testing.T) {
	for _, name := range []string{
		"[Unknown] Unseen Observatory v2 [10bit BD 1080p]",
		"[project-gxs] Unseen Observatory vX [10bit BD 1080p]",
		"[project-gxs] Unseen Observatory 2 [10bit BD 1080p]",
		"[project-gxs] Unseen Observatory v2 [10bit WEB 1080p]",
		"[SallySubs] Unseen Observatory [BD 1080p Unknown]",
		"[SallySubs] Unseen Observatory [BD 1080p FLAC] continuation",
		"[Unknown] Unseen Observatory [BD 1080p FLAC]",
		"[Cleo] Unseen Observatory | Other | Another [Dual Audio 10bit BD1080p][HEVC-x265]",
		"[Cleo] Unseen Observatory [Dual Audio 10bit WEB1080p][HEVC-x265]",
		"[Cleo] folder/Unseen Observatory [Dual Audio 10bit DVD480p][HEVC-x265]",
		"[Cleo] ../Unseen [Dual Audio 10bit DVD480p][HEVC-x265]",
		"[inid4c] Unseen Observatory v2 (1080p)",
		"[inid4c] Unseen Observatory 38 vX (1080p)",
		"[inid4c] Unseen Observatory 2045 v2 (1080p)",
		"[SallySubs] Unseen S01E02 [BD 1080p FLAC]",
		"[SallySubs] Unseen 1x02 [BD 1080p FLAC]",
		"[hi10p] We Tha Team [480p]",
		"[Coalgirls]_Katanagatari_(1280x720_Blu-Ray_FLAC)",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, mode := atamaFrozenLexical(name); mode != "" {
				t.Fatal("uncertified layout accepted")
			}
			if !reflect.DeepEqual(RecognizeRelease(name), recognizeReleaseEvidence(name)) || !reflect.DeepEqual(Recognize(name), recognizeFilenameEvidence(name)) {
				t.Fatal("countercontrol changed")
			}
		})
	}
}

func TestAtamaFrozenTitleMixedSlashNeverAuthorizesUnits(t *testing.T) {
	for _, name := range []string{

		"[Cleo] .unseen//Sign S01E02 [Dual Audio 10bit DVD480p][HEVC-x265]",
		"[Cleo] Unseen S01E02 | Alternate S02E04 [Dual Audio 10bit BD1080p][HEVC-x265]",
		"[Cleo] .unseen//Sign [Dual Audio 10bit DVD480p][HEVC-x265]/Other.S01E02.mkv",
		"[Watashi]_Unseen_S01E02_09_[576p][1234ABCD].mkv",
		"[inid4c] Unseen S01E02 WEB 01-12 (1080p)",
		"[inid4c] Unseen S01E02 38 v2 (1080p)",
		"[Commie] Unseen_2045 S01E02 - 07 [1234ABCD].mkv",
	} {
		t.Run(name, func(t *testing.T) {
			r := RecognizeRelease(name)
			m := Recognize(name)
			if !r.RequiresReview || len(r.UnitKeys) != 0 || len(r.Episodes) != 0 || r.Confidence != 0 || !m.RequiresReview || m.Confidence != 0 || m.Identity != nil {
				t.Fatalf("mixed scope authorized: %+v / %+v", r, m)
			}
			if r.RawName != name || r.ParserVersion != EpisodeParserVersion {
				t.Fatal("raw/version drift")
			}
		})
	}
	name := "[Commie] Unseen_2045 - 07 [1234ABCD].mkv"
	r := RecognizeRelease(name)
	if r.Title != "Unseen_2045" || r.Year != 0 || r.AbsoluteEpisode != 7 || !r.IsAnime || len(r.UnitKeys) != 0 || !r.RequiresReview || !strings.Contains(r.Reason, "mapping") {
		t.Fatalf("underscore title evidence lost: %+v", r)
	}
}
