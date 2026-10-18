package media

import (
	"github.com/google/uuid"
	"testing"
)

func TestStandardSingleEpisodeUsesCanonicalIdentityWithoutGuessing(t *testing.T) {
	u := NumberingUnit{ID: uuid.New(), EpisodeID: 100, Season: 2, Episode: 1, Key: "S02E01", Version: 7}
	r, reason := ResolveStandardSingleEpisode(RecognizeRelease("Fixture.2020.S02E01.1080p.WEB-DL.mkv"), []NumberingUnit{u})
	if len(r) != 1 || reason != "standard_coverage_only" || r[0].UnitID != u.ID || r[0].UnitVersion != 7 {
		t.Fatal(r, reason)
	}
	for _, name := range []string{"[Team] Fixture - 25 [1080p].mkv", "Fixture.S02E01E03.1080p.mkv", "Fixture.S02E01.sample.mkv", "Fixture.S02E01.first-half.mkv"} {
		if rows, _ := ResolveStandardSingleEpisode(RecognizeRelease(name), []NumberingUnit{u}); len(rows) != 0 {
			t.Fatal("unsafe coverage accepted", name, rows)
		}
	}
	for _, items := range [][]NumberingUnit{{}, {u, u}, {func() NumberingUnit { x := u; x.Retired = true; return x }()}, {func() NumberingUnit { x := u; x.EpisodeID = 0; return x }()}} {
		if rows, _ := ResolveStandardSingleEpisode(RecognizeRelease("Fixture.S02E01.1080p.mkv"), items); len(rows) != 0 {
			t.Fatal("unknown canonical evidence accepted", items)
		}
	}
}
