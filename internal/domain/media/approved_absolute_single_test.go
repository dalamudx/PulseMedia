package media

import (
	"github.com/google/uuid"
	"testing"
)

func TestApprovedAbsoluteSingleRequiresCanonicalFullMapping(t *testing.T) {
	id, other := uuid.New(), uuid.New()
	units := []NumberingUnit{{ID: id, EpisodeID: 1001, Key: "S02E01", Version: 3, Season: 2, Episode: 1}, {ID: other, EpisodeID: 1003, Key: "S02E03", Version: 2, Season: 2, Episode: 3}}
	rows := []NumberingRow{{SourceNumber: 25, EpisodeID: 1001, UnitID: id, UnitKey: "S02E01", UnitVersion: 3}, {SourceNumber: 27, EpisodeID: 1003, UnitID: other, UnitKey: "S02E03", UnitVersion: 2}}
	parsed := RecognizeRelease("[Team] Fixture - 25 [1080p]")
	got, reason := ResolveApprovedAbsoluteSingle(parsed, "absolute", rows, units)
	if len(got) != 1 || got[0].UnitID != id || reason != "mapped_coverage_only" {
		t.Fatal(got, reason)
	}
	if got, _ := ResolveApprovedAbsoluteSingle(RecognizeRelease("[Team] Fixture - 26 [1080p]"), "absolute", rows, units); len(got) != 0 {
		t.Fatal("gap guessed", got)
	}
	for _, change := range []string{"retired-other", "version-other", "provider-other", "unknown-provider", "duplicate-canonical"} {
		t.Run(change, func(t *testing.T) {
			copyUnits := append([]NumberingUnit(nil), units...)
			copyRows := append([]NumberingRow(nil), rows...)
			switch change {
			case "retired-other":
				copyUnits[1].Retired = true
			case "version-other":
				copyUnits[1].Version++
			case "provider-other":
				copyUnits[1].EpisodeID++
			case "unknown-provider":
				copyRows[0].EpisodeID = 0
			case "duplicate-canonical":
				copyUnits = append(copyUnits, units[0])
			}
			if got, _ := ResolveApprovedAbsoluteSingle(parsed, "manual", copyRows, copyUnits); len(got) != 0 {
				t.Fatal("partial mapping validity promoted authority", got)
			}
		})
	}
	for _, title := range []string{"[Team] Fixture - 25 [1080p] sample", "[Team] Fixture - 25 [1080p] - 27", "Fixture.S02E01 - 25"} {
		if got, _ := ResolveApprovedAbsoluteSingle(RecognizeRelease(title), "absolute", rows, units); len(got) != 0 {
			t.Fatal("ambiguous coverage accepted", title, got)
		}
	}
}
