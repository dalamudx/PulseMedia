package media

import (
	"github.com/google/uuid"
	"testing"
)

func TestResolveAbsoluteNumbering(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	rows := []NumberingRow{{SourceNumber: 25, UnitID: first, UnitKey: "S02E01", UnitVersion: 1}, {SourceNumber: 27, UnitID: second, UnitKey: "S02E03", UnitVersion: 1}}
	p := RecognizeRelease("[Team] Example - 25 [1080p].mkv")
	got, reason := ResolveAbsoluteNumbering(p, "manual", rows)
	if reason != "mapped_coverage_only" || len(got) != 1 || got[0].UnitID != first || p.Confidence != 0 || !p.RequiresReview || len(p.UnitKeys) > 0 {
		t.Fatal(got, reason, p)
	}
	for _, mode := range []string{"standard", "group", ""} {
		if got, _ := ResolveAbsoluteNumbering(p, mode, rows); len(got) > 0 {
			t.Fatal("unproven absolute mode", mode)
		}
	}
	p.ParserVersion = "episode.v7"
	if got, reason := ResolveAbsoluteNumbering(p, "manual", rows); len(got) > 0 || reason != "parser_version_changed" {
		t.Fatal(got, reason)
	}
	p = RecognizeRelease("[Team] Example - 25 [1080p].mkv")
	for _, bad := range []NumberingRow{{SourceNumber: 25, UnitID: second, UnitKey: "S02E03", UnitVersion: 1}, {SourceNumber: 29, UnitID: first, UnitKey: "S02E01", UnitVersion: 1}, {SourceNumber: 29, UnitID: uuid.New(), UnitKey: "S02E01junk", UnitVersion: 1}, {SourceNumber: 29, UnitID: uuid.New(), UnitKey: "S02E01", UnitVersion: 0}} {
		all := append(append([]NumberingRow(nil), rows...), bad)
		if got, reason := ResolveAbsoluteNumbering(p, "manual", all); len(got) > 0 || reason != "ambiguous_mapping" {
			t.Fatal(got, reason)
		}
	}
}
