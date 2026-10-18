package media

import (
	"github.com/google/uuid"
	"testing"
)

func TestReviewWholeStandardSeasonExplicitSet(t *testing.T) {
	// Synthetic boundary contract, independently specified; no gold-corpus claim.
	first := NumberingUnit{ID: uuid.MustParse("10000000-0000-0000-0000-000000000001"), EpisodeID: 1001, Season: 1, Episode: 1, Key: "S01E01", Version: 3}
	middle := NumberingUnit{ID: uuid.MustParse("10000000-0000-0000-0000-000000000002"), EpisodeID: 1002, Season: 1, Episode: 2, Key: "S01E02", Version: 7}
	last := NumberingUnit{ID: uuid.MustParse("10000000-0000-0000-0000-000000000003"), EpisodeID: 1003, Season: 1, Episode: 3, Key: "S01E03", Version: 2}
	full := []NumberingUnit{first, middle, last}
	scope := map[string]bool{"S01E01": true, "S01E02": true, "S01E03": true}
	tests := []struct {
		name, title, mode string
		season            int
		proof, canonical  []NumberingUnit
		selected          map[string]bool
		pass              bool
	}{
		{"explicit-whole", "Fixture.S01E01E02E03.1080p.WEB-DL", "standard", 1, full, full, scope, true},
		{"missing-middle", "Fixture.S01E01E03.1080p.WEB-DL", "standard", 1, full, full, scope, false},
		{"exact-noncontiguous-provider-proof", "Fixture.S01E01E03.1080p.WEB-DL", "standard", 1, []NumberingUnit{first, last}, []NumberingUnit{first, last}, scope, true},
		{"range-does-not-infer-proof", "Fixture.S01E01-E03.1080p.WEB-DL", "standard", 1, []NumberingUnit{first, last}, []NumberingUnit{first, last}, scope, false},
		{"no-proof", "Fixture.S01E01E02E03.1080p.WEB-DL", "standard", 1, nil, full, scope, false},
		{"single", "Fixture.S01E01.1080p.WEB-DL", "standard", 1, []NumberingUnit{first}, full, scope, false},
		{"special-season", "Fixture.S01E01E02E03.1080p.WEB-DL", "standard", 0, full, full, scope, false},
		{"nonstandard", "Fixture.S01E01E02E03.1080p.WEB-DL", "manual", 1, full, full, scope, false},
		{"unselected", "Fixture.S01E01E02E03.1080p.WEB-DL", "standard", 1, full, full, map[string]bool{"S01E01": true, "S01E03": true}, false},
	}
	for _, token := range []string{"COMPLETE", "pack", "OVA", "OAD", "specials"} {
		tests = append(tests, struct {
			name, title, mode string
			season            int
			proof, canonical  []NumberingUnit
			selected          map[string]bool
			pass              bool
		}{token, "Fixture.S01E01E02E03." + token + ".1080p.WEB-DL", "standard", 1, full, full, scope, false})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, reason := ReviewWholeStandardSeason(tt.title, RecognizeRelease(tt.title), tt.mode, tt.season, tt.proof, tt.canonical, tt.selected)
			if (len(rows) >= 2) != tt.pass {
				t.Fatal(tt.name, rows, reason)
			}
			if tt.pass && reason != "reviewed_not_executable" {
				t.Fatal(reason)
			}
		})
	}
}
