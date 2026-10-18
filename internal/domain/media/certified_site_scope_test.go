package media_test

import (
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

// These synthetic boundary probes do not contribute independent gold records.
func TestCertifiedSitePrefixKeepsScopeAndUnknownLabels(t *testing.T) {
	for _, prefix := range []string{"1337x.to - ", "(tracker.xn--p1ai) "} {
		for _, tail := range []string{"Revenge.2008.720p.X264-DIMENSION", "Movie.S01E02.720p.HDTV.x264-GROUP"} {
			got, baseline := media.RecognizeRelease(prefix+tail), media.RecognizeRelease(tail)
			if got.RawName != prefix+tail || got.Title != baseline.Title || got.Year != baseline.Year || got.MediaType != baseline.MediaType || got.Season != baseline.Season || strings.Join(got.UnitKeys, ",") != strings.Join(baseline.UnitKeys, ",") || got.Quality != baseline.Quality || got.RequiresReview != baseline.RequiresReview {
				t.Fatal("confirmed site changed identity, scope, raw input or quality", got, baseline)
			}
		}
		for _, tail := range []string{
			"Show.S01E02 / Film.2008.1080p.x264-GROUP",
			"Show.S01E02.S02E03.1080p.x264-GROUP",
			"Show.Ep.1-3 / Film.2008.1080p.x264-GROUP",
			"Show.2019.01.02 / Film.2008.1080p.x264-GROUP",
			"Film.1984.2009.1080p.x264-GROUP",
		} {
			got := media.RecognizeRelease(prefix + tail)
			if got.RawName != prefix+tail || !got.RequiresReview || len(got.UnitKeys) != 0 {
				t.Fatal("site hid competing years or complete raw scope", got)
			}
		}
	}
	for _, tc := range []struct{ raw, keep string }{
		{"1337x.to Revenge.2008.720p.X264-DIMENSION", "1337x"},
		{"1337x.too - Revenge.2008.720p.X264-DIMENSION", "1337x"},
		{"other1337x.to - Revenge.2008.720p.X264-DIMENSION", "other1337x"},
		{"(other.xn--p1ai) Revenge.2008.720p.X264-DIMENSION", "other"},
		{"(tracker.xn--p1ai.extra) Revenge.2008.720p.X264-DIMENSION", "tracker"},
		{"(tracker.xn--p1ai)Revenge.2008.720p.X264-DIMENSION", "tracker"},
		{"Revenge.1337x.to.2008.720p.X264-DIMENSION", "1337x"},
	} {
		got := media.RecognizeRelease(tc.raw)
		if got.RawName != tc.raw || !strings.Contains(got.Title, tc.keep) {
			t.Fatal("unknown or title-internal domain was removed", got)
		}
	}
	for _, raw := range []string{
		"1337x.to - Show.S01E02.E03.{tmdb-42}.1080p.x264-GROUP",
		"(tracker.xn--p1ai) Show.2019.01.02 / Film.{tmdb-42}.2008.1080p.x264-GROUP",
	} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatal("movie ID overrode full coverage", got)
		}
	}
}
