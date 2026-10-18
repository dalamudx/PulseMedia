package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// These date-bearing raw names are fixed MIT PTT originals, not generated
// golden entries: only their declared title/date/codec fields are upstream labels.
// This partial safety probe must not increase the full reviewed count.

// The first row is an unchanged MIT go-ptn original. Other rows are bounded
// adversarial syntax probes, not additional independently sourced goldens.
func TestTextualCalendarCoverageCannotBorrowReleaseYearOrMovieID(t *testing.T) {
	for _, raw := range []string{
		"WWE Monday Night Raw 3rd Nov 2014 HDTV x264-Sir Paul",
		"Fixture.3rd.Nov.2020.1080p.WEB-DL.x264.{tmdb-123}.mkv",
		"Fixture.November.3.2020.1080p.WEB-DL.x264.{tmdb-123}.mkv",
		"Fixture.31st.Feb.2020.1080p.WEB-DL.x264.{tmdb-123}.mkv",
	} {
		r := media.RecognizeRelease(raw)
		if !r.RequiresReview || r.Confidence != 0 || r.Year != 0 || len(r.UnitKeys) != 0 {
			t.Errorf("text date certified movie/year: %+v", r)
		}
		legacy := media.Recognize(raw)
		if !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Year != 0 || legacy.Identity != nil {
			t.Errorf("legacy ID hid text date: %+v", legacy)
		}
		if media.MatchesAutomaticMovie(raw, "Fixture", 2020, map[string]string{"tmdb.movie": "123"}) {
			t.Errorf("external ID authorized date-only coverage: %s", raw)
		}
	}
}
func TestTextualCalendarKeepsExplicitCanonicalUnitsAndSuffixProtection(t *testing.T) {
	for _, raw := range []string{
		"Fixture.S01E01.3rd.Nov.2020.1080p.WEB-DL.x264",
		"Fixture.S01E01.November.3.2020.1080p.WEB-DL.x264",
	} {
		r := media.RecognizeRelease(raw)
		if r.RequiresReview || r.Year != 0 || len(r.UnitKeys) != 1 || r.UnitKeys[0] != "S01E01" {
			t.Errorf("air-date year or lost canonical coverage: %+v", r)
		}
	}
	for _, raw := range []string{
		"Fixture.S01E01.3rd.Nov.2020-E03.1080p.WEB-DL.x264",
		"Fixture.S01E01.November.3.2020.02.1080p.WEB-DL.x264",
		"Fixture.S01E01.31st.Feb.2020.1080p.WEB-DL.x264",
		"Fixture.S01E01.11st.Nov.2020.1080p.WEB-DL.x264",
	} {
		r := media.RecognizeRelease(raw)
		if !r.RequiresReview || len(r.UnitKeys) != 0 {
			t.Errorf("invalid date/hidden extra coverage certified: %+v", r)
		}
	}
	if !media.MatchesAutomaticMovie("November.2004.1080p.WEB-DL.x264.{tmdb-123}.mkv", "November", 2004, map[string]string{"tmdb.movie": "123"}) {
		t.Error("single month word movie blocked")
	}
}

func TestCalendarOnlyCoverageCannotBecomeMovie(t *testing.T) {
	for _, raw := range []string{
		"Jimmy Kimmel 2017 05 03 720p HDTV DD5 1 MPEG2-CTL",
		"Stephen Colbert 2019 10 25 Eddie Murphy 480p x264-mSD [eztv]",
		"Jimmy.Fallon.2020.02.14.Steve.Buscemi.WEB.x264-XLF[TGx]",
		"Carson Daly 2017 09 13 Dan Harmon 720p HDTV x264-CROOKS",
	} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || got.Confidence != 0 || got.Year != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.Reason != "calendar_coverage_unresolved" {
			t.Errorf("calendar stamp became movie/year/unit: %+v", got)
		}
		movie := media.Recognize(raw)
		if !movie.RequiresReview || movie.Year != 0 || movie.Confidence != 0 || movie.Identity != nil {
			t.Errorf("legacy movie recognizer certified calendar coverage: %+v", movie)
		}
	}
}
func TestCalendarCoverageCannotHideBehindMovieID(t *testing.T) {
	for _, raw := range []string{
		"Fixture.2020.02.14.1080p.WEB-DL.x264.{tmdb-123}.mkv",
		"Fixture.20200214.1080p.WEB-DL.x264.{tmdb-123}.mkv",
		"Fixture.2020.02.30.1080p.WEB-DL.x264.{tmdb-123}.mkv",
		"Fixture.2020.13.03.1080p.WEB-DL.x264.{tmdb-123}.mkv",
		"Fixture.2020.02.14.S01E01.-.02.1080p.WEB-DL.x264.{tmdb-123}.mkv",
	} {
		if media.MatchesAutomaticMovie(raw, "Fixture", 2020, map[string]string{"tmdb.movie": "123"}) {
			t.Errorf("ID masked dated/invalid/mixed coverage: %s", raw)
		}
	}
}

func TestCalendarExclusionNeverHidesAdditionalCoverage(t *testing.T) {
	for _, raw := range []string{
		"Fixture.S01E01.2020.02.14.02.1080p.WEB-DL.x264",
		"Fixture.S01E01.2020.02.14-E03.1080p.WEB-DL.x264",
		"Fixture.S01E01.2020.02.30.1080p.WEB-DL.x264",
		"Fixture S01E01 2020 13 03 1080p WEB-DL x264",
		"Fixture.S01E01.2020.02.14.sample.1080p.WEB-DL.x264",
	} {
		r := media.RecognizeRelease(raw)
		if !r.RequiresReview || len(r.UnitKeys) != 0 || r.Confidence != 0 {
			t.Errorf("date hid invalid/additional coverage: %+v", r)
		}
	}
	raw := "Film.2020.1080p.BluRay.x264.{tmdb-20200214}.mkv"
	if !media.MatchesAutomaticMovie(raw, "Film", 2020, map[string]string{"tmdb.movie": "20200214"}) {
		t.Error("numeric external identity was misinterpreted as a date")
	}
}

func TestCanonicalEpisodeDateDoesNotSupplyWorkYear(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		year int
	}{
		{"Fixture.S01E01.2020.02.14.1080p.WEB-DL.x264", 0},
		{"Fixture.2014.S01E01.2020.02.14.1080p.WEB-DL.x264", 2014},
		{"Fixture.2014.1x01.2020.02.14.1080p.WEB-DL.x264", 2014},
		{"Fixture.S01E01.2020.02.14.2020.02.15.1080p.WEB-DL.x264", 0},
	} {
		got := media.RecognizeRelease(tc.raw)
		if got.RequiresReview || got.Year != tc.year || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "S01E01" {
			t.Errorf("air date became work year or invalidated explicit canonical unit: %+v", got)
		}
	}
}
