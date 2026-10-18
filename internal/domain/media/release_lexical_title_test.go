package media

import (
	"slices"
	"testing"
)

// Controls are hand-authored counterexamples, not additional independent gold.
func TestReleaseLexicalTitleControls(t *testing.T) {
	for _, row := range []struct {
		input, title string
		year         int
	}{
		{"Orbit☆Night ★Hi10★ [1080p]", "Orbit☆Night", 0},
		{"Morning 8/12 (2001) 720p", "Morning 8/12", 2001},
		{"16/9 2001 WEB-DL", "16/9", 2001},
		{"Река / River / Der Fluss (Иван / John) [2001, Страна, драма, BDRip]", "River / Der Fluss", 2001},
		{"Река / Der Fluss / River (Hans Müller) [2001, Страна, драма, DVDRip]", "Der Fluss / River", 2001},
	} {
		t.Run(row.input, func(t *testing.T) {
			if got, ok := releaseLexicalTitleView(row.input, row.year); !ok || got != row.title {
				t.Fatalf("view = %q, %v; want %q", got, ok, row.title)
			}
		})
	}
	for _, row := range []struct {
		input string
		year  int
	}{
		{"[Unknown] Orbit☆Night ★Hi10★ [1080p]", 0},
		{"Orbit [Mystery] ★Hi10★ [1080p]", 0},
		{"Orbit ★Hi10★ [Mystery]", 0},
		{"Orbit ★Hi10★ [1080p] Continuation", 0},
		{"Orbit Hi10 [1080p]", 0},
		{"Orbit ★Hi10 [1080p]", 0},
		{"Web Cam 2001 720p", 2001},
		{"Web Cam [720p]", 0},
		{"/media/Morning 8/12 (2001) 720p", 2001},
		{"folder/Morning 8/12 (2001) 720p", 2001},
		{"Morning 8 / 12 (2001) 720p", 2001},
		{"Morning 8/12 (2001 720p", 2001},
		{"Morning 8/12 2001) 720p", 2001},
		{"Morning 8/12 (2001) 720p [Unknown]", 2001},
		{"Morning 8/12 (2001) 720p S01E02", 2001},
		{"Morning 8/12 (2001) 720p Eps 1-3", 2001},
		{"Morning 8/12 (2001) 720p sample", 2001},
		{"Morning 8/12 (2001) 720p 2002", 2001},
		{"Morning 8/12 (2001) 720p", 0},
		{"[Unknown] Река / River (Иван / John) [2001, Страна, драма, BDRip]", 2001},
		{"Река / River (Иван / John) [2001, Страна, драма, BDRip][Unknown]", 2001},
		{"Река / River (Иван / John [2001, Страна, драма, BDRip]", 2001},
		{"Река / River (Иван / John) [2001, Страна, драма, UnknownRip]", 2001},
		{"Река / River (Иван / John) [2001, Страна, драма, BDRip] 2002", 2001},
		{"Река (River) 2001 BDRip", 2001},
		{"[Unknown][River / Fluss][Movie][BDRip][1080P]", 0},
	} {
		t.Run("decline/"+row.input, func(t *testing.T) {
			if got, ok := releaseLexicalTitleView(row.input, row.year); ok || got != "" {
				t.Fatalf("unreviewed layout repaired: %q, %v", got, ok)
			}
		})
	}
}

func TestReleaseLexicalAdmissionControls(t *testing.T) {
	for _, row := range []struct {
		input string
		year  int
	}{
		{"Web Cam (2001) 720p", 2001},
		{"Orbit☆Night ★Hi10★ [1080p]", 0},
		{"Morning 8/12 (2001) 720p", 2001},
	} {
		t.Run(row.input, func(t *testing.T) {
			got := RecognizeRelease(row.input)
			if got.RawName != row.input || got.Year != row.year {
				t.Fatalf("raw/year altered: %+v", got)
			}
			if got.Season != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
				t.Fatalf("coverage invented: %+v", got)
			}
			if row.year == 0 && (!got.RequiresReview || got.Confidence != .6 || len(got.UnitKeys) != 0) {
				t.Fatalf("yearless authority changed: %+v", got)
			}
			if row.input == "Web Cam (2001) 720p" && got.Title != "Web Cam" {
				t.Fatalf("ordinary words removed: %q", got.Title)
			}
		})
	}
	for _, input := range []string{
		"Morning 8/12 (2001) 720p Eps 1-3",
		"Morning 8/12 (2001) 720p sample",
		"S01E02/Morning 8/12 (2001) 720p",
		"Река S01E02 / River (Иван / John) [2001, Страна, драма, BDRip]",
		"Река / River (Иван / John) [2001, Страна, драма, BDRip] Eps 1-3",
		"Orbit S01E02 ★Hi10★ [1080p] sample",
		"Orbit 2001 2002 ★Hi10★ [1080p]",
	} {
		t.Run("hold/"+input, func(t *testing.T) {
			got := RecognizeRelease(input)
			if got.RawName != input || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 {
				t.Fatalf("raw coverage refusal changed: %+v", got)
			}
		})
	}
	// A standard single episode is existing numbering evidence, not a refusal.
	single := "Morning 8/12 (2001) 720p S01E02"
	one := RecognizeRelease(single)
	if one.RawName != single || one.Season != 1 || !slices.Equal(one.Episodes, []int{2}) || !slices.Equal(one.UnitKeys, []string{"S01E02"}) {
		t.Fatalf("standard single coverage changed: %+v", one)
	}
	// An explicit set is kept exactly; no implicit season or gap filling.
	input := "Orbit S01E01E03 ★Hi10★ [1080p]"
	got := RecognizeRelease(input)
	if got.RawName != input || got.Season != 1 || !slices.Equal(got.Episodes, []int{1, 3}) || !slices.Equal(got.UnitKeys, []string{"S01E01", "S01E03"}) {
		t.Fatalf("explicit coverage changed: %+v", got)
	}
}
