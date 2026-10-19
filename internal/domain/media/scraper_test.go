package media_test

import (
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestMovieAndTVNFOGeneration(t *testing.T) {
	// 1. Generate Movie NFO
	movieMeta := media.MovieNFOData{
		TMDBID:        550,
		IMDbID:        "tt0137523",
		Title:         "Fight Club",
		OriginalTitle: "Fight Club",
		Year:          1999,
		Plot:          "An insomniac office worker forms an underground fight club.",
		Genres:        []string{"Drama", "Thriller"},
	}

	nfoBytes, err := media.GenerateMovieNFO(movieMeta)
	if err != nil {
		t.Fatalf("generate movie nfo failed: %v", err)
	}

	xmlStr := string(nfoBytes)
	if !strings.Contains(xmlStr, "<title>Fight Club</title>") ||
		!strings.Contains(xmlStr, `<uniqueid type="tmdb" default="true">550</uniqueid>`) ||
		!strings.Contains(xmlStr, "<year>1999</year>") {
		t.Fatalf("invalid generated nfo xml: %s", xmlStr)
	}

	// 2. Generate TV Show NFO
	tvMeta := media.TVShowNFOData{
		TMDBID: 1396,
		Title:  "Breaking Bad",
		Year:   2008,
		Plot:   "A chemistry teacher diagnosed with terminal lung cancer.",
	}

	tvNfoBytes, err := media.GenerateTVShowNFO(tvMeta)
	if err != nil {
		t.Fatalf("generate tv nfo failed: %v", err)
	}
	if !strings.Contains(string(tvNfoBytes), "<tvshow>") || !strings.Contains(string(tvNfoBytes), "Breaking Bad") {
		t.Fatalf("invalid generated tv nfo: %s", string(tvNfoBytes))
	}
}
