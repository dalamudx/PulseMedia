package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Fixed MIT PTT collection originals provide partial safety evidence, not new
// full-golden approvals or proof of canonical works/units.
func TestMovieCollectionCannotBecomeSingleUnit(t *testing.T) {
	for _, raw := range []string{
		"Harry.Potter.Complete.Collection.2001-2011.1080p.BluRay.DTS-ETRG",
		"Game of Thrones All 7 Seasons 1080p ~∞~ .HakunaMaKoko",
		"Avatar: The Last Airbender Full Series 720p",
		"Dora the Explorer - Ultimate Collection",
		"Mr Bean Complete Pack (Animated, Tv series, 2 Movies) DVDRIP (WA",
		"American Pie - Complete set (8 movies) 720p mkv - YIFY",
	} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || len(got.UnitKeys) != 0 || got.Confidence != 0 {
			t.Errorf("explicit collection gained single-unit authority: %+v", got)
		}
	}
}

func TestExplicitCompoundMovieCollectionCannotUseYearOrID(t *testing.T) {
	for _, marker := range []string{"Complete.Collection", "Complete.Pack", "Complete.Set", "Full.Series", "All.3.Seasons", "8.Movies", "Box.Set"} {
		raw := "Fixture." + marker + ".2020.1080p.BluRay.x264.{tmdb-550}.mkv"
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 {
			t.Errorf("year/ID certified collection %s: %+v", marker, got)
		}
		old := media.Recognize(raw)
		if !old.RequiresReview || old.Confidence != 0 || old.Identity != nil {
			t.Errorf("legacy year/ID certified collection %s: %+v", marker, old)
		}
	}
}

// Morphological multi-work suffixes are explicit partial safety probes, not goldens.
func TestNumberedCollectionSuffixHasNoMovieAuthority(t *testing.T) {
	for _, marker := range []string{"Duology", "Trilogy", "Tetralogy", "Quadrilogy", "Quadrology", "Pentalogy", "Hexalogy"} {
		raw := "Fixture." + marker + ".2020.1080p.BluRay.x264.{tmdb-550}.mkv"
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 {
			t.Errorf("multi-work suffix gained movie authority: %s", marker)
		}
		old := media.Recognize(raw)
		if !old.RequiresReview || old.Confidence != 0 || old.Identity != nil {
			t.Errorf("legacy identity bypassed multi-work suffix: %s", marker)
		}
		if media.MatchesAutomaticMovie(raw, "Fixture", 2020, map[string]string{"tmdb.movie": "550"}) {
			t.Errorf("explicit ID bypassed multi-work suffix: %s", marker)
		}
	}
}

func TestNumberedCollectionIDWithoutYearCannotCertifyOneMovie(t *testing.T) {
	for _, tail := range []string{"{tmdb-550}.1080p.BluRay.x264", "1080p.BluRay.x264.{tmdb-550}"} {
		raw := "Fixture.Quadrilogy." + tail + ".mkv"
		got := media.RecognizeRelease(raw)
		old := media.Recognize(raw)
		if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || !old.RequiresReview || old.Identity != nil {
			t.Errorf("no-year identity certified a multi-work suffix: %s", raw)
		}
	}
}

func TestCollectionMorphologyDoesNotCertifyOrdinaryTitlesAsPacks(t *testing.T) {
	for _, raw := range []string{"Trilogy.of.Terror.1975.1080p.BluRay.x264.mkv", "Trilogy.1969.1080p.BluRay.x264.mkv", "The.Trilogy.2020.1080p.BluRay.x264.mkv"} {
		got := media.RecognizeRelease(raw)
		if got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Errorf("ordinary movie title turned into multi-work declaration: %s", raw)
		}
	}
}

func TestCollectionWordMovieTitleKeepsOriginalContract(t *testing.T) {
	for _, raw := range []string{"The.Collection.2012.1080p.BluRay.x264.mkv", "Collection.2012.1080p.BluRay.x264.mkv", "Complete.2012.1080p.BluRay.x264.mkv", "Full.Metal.Jacket.1987.1080p.BluRay.x264.mkv"} {
		got := media.RecognizeRelease(raw)
		if got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Errorf("single word title incorrectly became collection: %+v", got)
		}
	}
}

func TestFrenchIntegralPackageCannotCertifyOneMovie(t *testing.T) {
	for _, raw := range []string{
		"Friends.1994.INTEGRALE.MULTI.1080p.WEB-DL.H265-FTMVHD",
		"Fixture.2020.INTEGRALE.MULTI.1080p.BluRay.x264.{tmdb-550}.mkv",
		"Fixture.2020.INTÉGRALE.MULTI.1080p.BluRay.x264.{tmdb-550}.mkv",
		"Fixture.2020.INTEGRAL.MULTI.COMPLETE.1080p.BluRay.x264.{tmdb-550}.mkv",
		"Fixture.2020.MULTI.INTEGRALE.1080p.BluRay.x264.{tmdb-550}.mkv",
	} {
		got, old := media.RecognizeRelease(raw), media.Recognize(raw)
		if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || !old.RequiresReview || old.Identity != nil {
			t.Errorf("French package gained one-movie authority %q: %+v / %+v", raw, got, old)
		}
		if media.MatchesAutomaticMovie(raw, "Fixture", 2020, map[string]string{"tmdb.movie": "550"}) {
			t.Errorf("ID overrode a French complete package %q", raw)
		}
	}
}

func TestIntegralMovieTitlesAreNotFrenchPackages(t *testing.T) {
	for _, raw := range []string{"Integral.2020.MULTI.1080p.BluRay.x264.mkv", "Intégrale.2020.MULTI.1080p.BluRay.x264.mkv", "The.Integral.2020.1080p.BluRay.x264.mkv"} {
		got := media.RecognizeRelease(raw)
		if got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Errorf("ordinary Integral film became a package %q: %+v", raw, got)
		}
	}
}
