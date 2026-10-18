package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
	"testing"
)

func TestNumberingIdentityJoinNeverRewritesCanonicalOrExpandsGaps(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	units := []media.NumberingUnit{{ID: a, EpisodeID: 1001, Season: 1, Episode: 1, Key: "S01E01", Version: 3}, {ID: b, EpisodeID: 1002, Season: 2, Episode: 1, Key: "S02E01", Version: 4}}
	src := []media.NumberingEpisode{{SourceNumber: 27, EpisodeID: 1002, Season: 2, Episode: 1}, {SourceNumber: 25, EpisodeID: 1001, Season: 1, Episode: 1}}
	rows, why := media.BuildEpisodeNumbering(src, units)
	if why != "" || len(rows) != 2 || rows[0].SourceNumber != 25 || rows[0].UnitID != a || rows[1].SourceNumber != 27 || rows[1].UnitID != b {
		t.Fatal(rows, why)
	}
	for _, name := range []string{"missing", "retired", "renumbered", "duplicate", "zero"} {
		t.Run(name, func(t *testing.T) {
			u := append([]media.NumberingUnit(nil), units...)
			s := append([]media.NumberingEpisode(nil), src...)
			switch name {
			case "missing":
				u[1].EpisodeID = 0
			case "retired":
				u[1].Retired = true
			case "renumbered":
				s[0].Season = 1
			case "duplicate":
				s[1].SourceNumber = s[0].SourceNumber
			case "zero":
				s[0].EpisodeID = 0
			}
			r, reason := media.BuildEpisodeNumbering(s, u)
			if reason == "" || len(r) != 0 {
				t.Fatal("unsafe partial authority", r, reason)
			}
		})
	}
}
