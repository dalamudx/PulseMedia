package metadatafallback_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatafallback"
)

func TestTVTranslationSupplementPreservesCanonicalFacts(t *testing.T) {
	for _, scenario := range []string{"both_missing", "overview_missing", "title_missing", "explicit_latin_title", "source_empty"} {
		t.Run(scenario, func(t *testing.T) {
			primary := &ports.TVDetails{TMDBID: 1396, Title: "One Piece", OriginalTitle: "original", Overview: "primary plot", Year: 2008, Rating: 8, Genres: []string{"Drama"}, SeasonsCount: 1, EpisodesCount: 2, Seasons: []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 2, AirDate: "2008-01-20"}}}
			original := *primary
			local := &ports.TVTranslation{TMDBID: 1396, Language: "zh-CN"}
			source := &ports.TVDBTranslation{TVDBID: 81189, Language: "zho", Title: "备用中文名", Overview: "备用简介"}
			switch scenario {
			case "overview_missing":
				local.Title = "One Piece"
			case "title_missing":
				local.Overview = "primary plot"
			case "explicit_latin_title":
				local.Title = "One Piece"
				local.Overview = "primary plot"
			case "source_empty":
				source.Title, source.Overview = "", ""
			}
			result, err := metadatafallback.SupplementTranslation(primary, local, ports.TVArtworkIdentity{TMDBID: 1396, TVDBID: 81189}, source)
			if err != nil || result == nil {
				t.Fatal(err)
			}
			expected := original
			if local.Title == "" && source.Title != "" {
				expected.Title = source.Title
			}
			if local.Overview == "" && source.Overview != "" {
				expected.Overview = source.Overview
			}
			provenance := result.TranslationSource
			result.TranslationSource = nil
			if !reflect.DeepEqual(*result, expected) || !reflect.DeepEqual(*primary, original) {
				t.Fatal("translation changed canonical facts or primary cache")
			}
			if expected.Title == original.Title && expected.Overview == original.Overview {
				if provenance != nil {
					t.Fatal("claimed unused source")
				}
			} else if provenance == nil || provenance.Provider != "tvdb" || provenance.SourceID != 81189 || provenance.Language != "zho" || len(provenance.Fields) == 0 {
				t.Fatal("missing independent source evidence")
			}
			result.Genres[0] = "changed"
			result.Seasons[0].EpisodeCount = 99
			if primary.Genres[0] != "Drama" || primary.Seasons[0].EpisodeCount != 2 {
				t.Fatal("output aliases primary facts")
			}
		})
	}
}
func TestTVTranslationSupplementRejectsIdentityAndLanguageMismatch(t *testing.T) {
	for _, scenario := range []string{"local_id", "local_language", "mapping_id", "source_id", "source_language"} {
		t.Run(scenario, func(t *testing.T) {
			primary := &ports.TVDetails{TMDBID: 1396, Title: "original"}
			local := &ports.TVTranslation{TMDBID: 1396, Language: "zh-CN"}
			mapping := ports.TVArtworkIdentity{TMDBID: 1396, TVDBID: 81189}
			source := &ports.TVDBTranslation{TVDBID: 81189, Language: "zho", Title: "补充"}
			switch scenario {
			case "local_id":
				local.TMDBID++
			case "local_language":
				local.Language = "zh-TW"
			case "mapping_id":
				mapping.TMDBID++
			case "source_id":
				source.TVDBID++
			case "source_language":
				source.Language = "eng"
			}
			if result, err := metadatafallback.SupplementTranslation(primary, local, mapping, source); result != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal("unproven translation accepted", err)
			}
		})
	}
}
