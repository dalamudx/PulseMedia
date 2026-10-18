package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
	"testing"
)

func TestNumberingRejectsCanonicalAliasesAndInvalidKeys(t *testing.T) {
	for _, name := range []string{"negative-season", "zero-episode", "alias-uuid", "alias-key"} {
		t.Run(name, func(t *testing.T) {
			a, b := uuid.New(), uuid.New()
			units := []media.NumberingUnit{{ID: a, EpisodeID: 101, Season: 1, Episode: 1, Key: "S01E01", Version: 1}, {ID: b, EpisodeID: 102, Season: 1, Episode: 2, Key: "S01E02", Version: 1}}
			source := []media.NumberingEpisode{{SourceNumber: 25, EpisodeID: 101, Season: 1, Episode: 1}, {SourceNumber: 27, EpisodeID: 102, Season: 1, Episode: 2}}
			switch name {
			case "negative-season":
				source[1].Season = -1
				units[1].Season = -1
				units[1].Key = media.FormatEpisodeUnitKey(-1, 2)
			case "zero-episode":
				source[1].Episode = 0
				units[1].Episode = 0
				units[1].Key = media.FormatEpisodeUnitKey(1, 0)
			case "alias-uuid":
				units[1].ID = a
			case "alias-key":
				units[1].Key = "S01E01"
				units[1].Episode = 1
				source[1].Episode = 1
			}
			rows, reason := media.BuildEpisodeNumbering(source, units)
			if len(rows) != 0 || reason == "" {
				t.Fatal("invalid canonical set issued mapping", rows, reason)
			}
		})
	}
}
