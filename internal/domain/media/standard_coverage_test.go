package media

import (
	"github.com/google/uuid"
	"math"
	"testing"
)

func TestCompleteStandardCoverageNeverGuessesOrReturnsPartialSet(t *testing.T) {
	units := []NumberingUnit{{ID: uuid.New(), EpisodeID: 101, Season: 1, Episode: 1, Key: "S01E01", Version: 1}, {ID: uuid.New(), EpisodeID: 103, Season: 1, Episode: 3, Key: "S01E03", Version: 2}}
	parsed := RecognizeRelease("Fixture.S01E01E03.1080p.WEB-DL.x264")
	rows, reason := ResolveStandardCoverage(parsed, units)
	if len(rows) != 2 || reason != "standard_multi_coverage_only" || rows[0].UnitID != units[0].ID || rows[1].UnitID != units[1].ID {
		t.Fatal(parsed, rows, reason)
	}
	if rows, _ := ResolveStandardSingleEpisode(parsed, units); len(rows) != 0 {
		t.Fatal("multi explanation bypassed single execution")
	}
	for _, test := range []string{"unknown-key", "retired", "version", "provider", "duplicate-provider", "duplicate-key", "ambiguous-key", "low-confidence", "nan", "fragment", "pack", "absolute"} {
		t.Run(test, func(t *testing.T) {
			p := parsed
			u := append([]NumberingUnit(nil), units...)
			switch test {
			case "unknown-key":
				u = u[:1]
			case "retired":
				u[1].Retired = true
			case "version":
				u[1].Version = 0
			case "provider":
				u[1].EpisodeID = 0
			case "duplicate-provider":
				u[1].EpisodeID = u[0].EpisodeID
			case "duplicate-key":
				p.UnitKeys = []string{"S01E01", "S01E01"}
			case "ambiguous-key":
				u = append(u, units[1])
			case "low-confidence":
				p.Confidence = 0.4
			case "nan":
				p.Confidence = math.NaN()
			case "fragment":
				p = RecognizeRelease("Fixture.S01E01E03.sample")
			case "pack":
				p = RecognizeRelease("Fixture.S01.Complete.1080p")
			case "absolute":
				p = RecognizeRelease("[Team] Fixture - 25 [1080p]")
			}
			if rows, _ := ResolveStandardCoverage(p, u); len(rows) != 0 {
				t.Fatal("partial/guessed complete set", p, rows)
			}
		})
	}
	rangeParsed := RecognizeRelease("Fixture.S01E01-E03.1080p")
	if rows, _ := ResolveStandardCoverage(rangeParsed, units); len(rows) != 0 {
		t.Fatal("missing E02 silently skipped", rows)
	}
}
