package media

import (
	"reflect"
	"testing"
)

func TestMovieSearchIdentityRequiresCompleteUniqueEvidence(t *testing.T) {
	rec := Recognize("Fight.Club.1999.1080p.BluRay.x264.mkv")
	known := MovieSearchIdentity{TMDBID: 550, Title: "Fight Club", Year: 1999, MediaType: "movie"}
	for _, tc := range []struct {
		name       string
		candidates []MovieSearchIdentity
		complete   bool
		index      int
		ok         bool
	}{
		{"exact", []MovieSearchIdentity{known}, true, 0, true},
		{"original_title_after_unrelated_result", []MovieSearchIdentity{{TMDBID: 999, Title: "Other Film", Year: 1999}, {TMDBID: 550, Title: "搏击俱乐部", OriginalTitle: "FIGHT-CLUB", Year: 1999}}, true, 1, true},
		{"other_year_then_exact", []MovieSearchIdentity{{TMDBID: 999, Title: "Fight Club", Year: 2000}, known}, true, 1, true},
		{"fuzzy_title", []MovieSearchIdentity{{TMDBID: 550, Title: "Fight Club Part Two", Year: 1999}}, true, -1, false},
		{"wrong_year", []MovieSearchIdentity{{TMDBID: 550, Title: "Fight Club", Year: 2000}}, true, -1, false},
		{"ambiguous", []MovieSearchIdentity{known, {TMDBID: 999, Title: "Fight Club", Year: 1999}}, true, -1, false},
		{"duplicate_identity", []MovieSearchIdentity{known, known}, true, -1, false},
		{"unknown_year_competitor", []MovieSearchIdentity{known, {TMDBID: 999, Title: "Fight Club"}}, true, -1, false},
		{"invalid_id", []MovieSearchIdentity{known, {Title: "Other Film", Year: 1999}}, true, -1, false},
		{"wrong_type", []MovieSearchIdentity{{TMDBID: 550, Title: "Fight Club", Year: 1999, MediaType: "tv"}}, true, -1, false},
		{"incomplete", []MovieSearchIdentity{known}, false, -1, false},
		{"empty", nil, true, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]MovieSearchIdentity(nil), tc.candidates...)
			index, ok := SelectMovieSearchIdentity(rec, tc.candidates, tc.complete)
			if index != tc.index || ok != tc.ok || !reflect.DeepEqual(before, tc.candidates) {
				t.Fatalf("selection=%d/%v expected=%d/%v or changed input", index, ok, tc.index, tc.ok)
			}
		})
	}
	for _, raw := range []string{"Fight.Club.1080p.BluRay.x264.mkv", "Fight.Club.1999.{tmdb-550}.mkv", "Fight.Club.S01.1999.mkv"} {
		if _, ok := SelectMovieSearchIdentity(Recognize(raw), []MovieSearchIdentity{known}, true); ok {
			t.Fatalf("unsupported recognition promoted: %s", raw)
		}
	}
}
