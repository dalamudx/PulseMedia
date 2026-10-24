package naming_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestMediaTemplatesPreserveLegacyDefaults(t *testing.T) {
	for _, input := range []naming.MediaTemplateInput{
		{Title: "Fixture", Year: 2024, Season: 1, Episode: 7, Extension: ".mkv", Quality: quality.Profile{Resolution: quality.Res1080p}},
		{Title: "A: Film", Season: 0, Episode: 2, Extension: "mp4"},
	} {
		rules := naming.TemplateRules{}
		got, err := rules.MovieRelative(input)
		expected, wantErr := naming.CleanRelPath(filepath.Join(naming.FormatMovieDir(input.Title, input.Year), naming.FormatMovieFile(input.Title, input.Year, input.Quality, input.Extension)))
		if got != expected || (err == nil) != (wantErr == nil) {
			t.Fatal("default movie output changed", got, err)
		}
		got, err = rules.EpisodeRelative(input)
		expected, wantErr = naming.FormatEpisodeRel(input.Title, input.Year, input.Season, input.Episode, input.Quality, input.Extension)
		if got != expected || (err == nil) != (wantErr == nil) {
			t.Fatal("default episode output changed", got, err)
		}
	}
}

func TestMediaTemplatesRenderKnownFactsWithoutMutatingInput(t *testing.T) {
	input := naming.MediaTemplateInput{Title: "Fixture {year}", Year: 2024, Season: 1, Episode: 7, Extension: ".MKV", TMDBID: 42, IMDBID: "tt1234567", Quality: quality.Profile{
		Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion,
		Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion,
		Codec: quality.CodecHEVC, CodecParserVersion: quality.CodecTokenParserVersion,
		HDR: quality.HDRDolbyVision, HDRParserVersion: quality.HDRTokenParserVersion,
		Audio: "AAC", Edition: "Extended", Group: "FixtureGroup",
	}}
	original := input
	rules := naming.TemplateRules{MovieTemplate: "{title} ({year}) [{tmdb_id} {imdb_id}]/{title} [{source} {resolution} {codec} {hdr} {audio} {edition} {group}].{extension}", EpisodeTemplate: "{title}/Season {season}/{episode_key} - {episode}.{extension}"}
	got, err := rules.MovieRelative(input)
	expected := "Fixture {year} (2024) [42 tt1234567]/Fixture {year} [WEB-DL 1080p HEVC DV AAC Extended FixtureGroup].mkv"
	if err != nil || got != expected {
		t.Fatal("known facts or literal metadata changed", got, err)
	}
	got, err = rules.EpisodeRelative(input)
	if err != nil || got != "Fixture {year}/Season 01/S01E07 - 07.mkv" {
		t.Fatal("sparse episode replaced", got, err)
	}
	input.Season = 0
	got, err = rules.EpisodeRelative(input)
	if err != nil || !strings.Contains(got, "S00E07") {
		t.Fatal("explicit specials lost", got, err)
	}
	input.Season = 1
	if !reflect.DeepEqual(input, original) {
		t.Fatal("formatter mutated input")
	}
}

func TestMediaTemplatesNeverInventMissingFacts(t *testing.T) {
	for _, field := range []string{"year", "tmdb_id", "imdb_id", "resolution", "source", "codec", "hdr", "audio", "edition", "group"} {
		t.Run(field, func(t *testing.T) {
			input := naming.MediaTemplateInput{Title: "Fixture", Extension: ".mkv", Quality: quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceWEBDL, Codec: quality.CodecHEVC, HDR: quality.HDRDolbyVision}}
			rules := naming.TemplateRules{MovieTemplate: "{title} [{" + field + "}].{extension}"}
			if _, err := rules.MovieRelative(input); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
				t.Fatal("missing or unverified evidence invented", err)
			}
		})
	}
	for _, episode := range []int{0, -1, 10000} {
		if _, err := (naming.TemplateRules{EpisodeTemplate: "{title}.{extension}"}).EpisodeRelative(naming.MediaTemplateInput{Title: "Fixture", Extension: "mkv", Season: 1, Episode: episode}); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
			t.Fatal("unknown episode accepted", err)
		}
	}
	// Evidence for one property does not manufacture evidence for another.
	input := naming.MediaTemplateInput{Title: "Fixture", Extension: ".mkv", Quality: quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion}}
	if got, err := (naming.TemplateRules{MovieTemplate: "{title} [{resolution}].{extension}"}).MovieRelative(input); err != nil || got != "Fixture [1080p].mkv" {
		t.Fatal("independent verified fact unavailable", got, err)
	}
	if _, err := (naming.TemplateRules{MovieTemplate: "{title} [{source}].{extension}"}).MovieRelative(input); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
		t.Fatal("independent unknown source guessed", err)
	}
}

func TestMediaTemplatesPreserveContainerAndFiltering(t *testing.T) {
	for _, test := range []struct{ template, extension string }{
		{"{title}.mkv", ".mp4"}, {"{title}.srt", ".mkv"}, {"{title}", ".mkv"},
		{"{title}/sample.{extension}", ".mkv"}, {"trailers/{title}.{extension}", ".mkv"},
		{"{title}.{extension}", ".movie.mkv"}, {"{title}.{extension}", ".srt"},
	} {
		if _, err := (naming.TemplateRules{MovieTemplate: test.template}).MovieRelative(naming.MediaTemplateInput{Title: "Fixture", Extension: test.extension}); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
			t.Fatal("template changed container or bypassed media filtering", err)
		}
	}
	if got, err := (naming.TemplateRules{MovieTemplate: "{title}.MKV"}).MovieRelative(naming.MediaTemplateInput{Title: "Fixture", Extension: ".mkv"}); err != nil || got != "Fixture.MKV" {
		t.Fatal("case-equivalent container rejected", got, err)
	}
}
