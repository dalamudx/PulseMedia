package media_test

import (
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestAutomaticTVIdentityStrictCatalogEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, raw, title, original string
		year                       int
	}{
		{"localized", "漫长的季节.2023.S01E03.1080p.WEB-DL.mkv", "漫长的季节", "The Long Season", 2023},
		{"original alias", "The.Knockout.2023.S01E03.1080p.WEB-DL.mkv", "狂飙", "The Knockout", 2023},
		{"punctuation and case", "MARVELS_Agents-of-SHIELD.(2013).S02E03.mkv", "Marvels Agents of SHIELD", "", 2013},
		{"punctuation equivalence", "Marvel's.Agents.of.S.H.I.E.L.D.2013.S02E03.mkv", "Marvel’s Agents of S H I E L D", "", 2013},
		{"prefix year", "The.Flash.(2014).S01E04.1080p.HDTV.x264", "The Flash", "", 2014},
		{"alternate standard numbering", "Doctor.Who.(2005).8x11.720p.HDTV.x264", "Doctor Who", "", 2005},
		{"sparse episode", "Fixture.2020.S01E03.mkv", "Fixture", "", 2020},
		{"four digit episode", "Fixture.2020.S01E1056.mkv", "Fixture", "", 2020},
		{"no quality needed", "Fixture.2020.S01E03", "Fixture", "", 2020},
		{"matching tag still needs evidence", "Fixture.2020.S01E03.{tmdb-100}.mkv", "Fixture", "", 2020},
		{"tag number is not year", "Fixture.{tmdb-100}.2020.S01E03.mkv", "Fixture", "", 2020},
		{"certified publisher", "[DKB] Fixture.2020.S01E03.1080p.mkv", "Fixture", "", 2020},
		{"episode air date differs", "Fixture.2014.S01E03.2020.02.14.1080p.mkv", "Fixture", "", 2014},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := media.DecideAutomaticTVIdentity(tc.raw, tc.title, tc.original, tc.year, 100)
			assertAutomaticTVIdentity(t, got, "eligible", "strict_title_year")
		})
	}
}

func TestAutomaticTVIdentityHoldsOrRejectsUnprovenEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, raw, state, reason string
	}{
		{"wrong title", "Other.2020.S01E03.mkv", "hold", "media_title_unproven"},
		{"extra title text", "Fixture.Extra.2020.S01E03.mkv", "hold", "media_title_unproven"},
		{"missing year", "Fixture.S01E03.mkv", "hold", "media_identity_unproven"},
		{"wrong year", "Fixture.2021.S01E03.mkv", "reject", "media_year_conflict"},
		{"wrong tmdb", "Fixture.2020.S01E03.{tmdb-999}.mkv", "reject", "media_identity_conflict"},
		{"conflicting tmdb", "Fixture.2020.S01E03.{tmdb-100}.{tmdb-999}.mkv", "reject", "media_identity_conflict"},
		{"overflow tmdb", "Fixture.2020.S01E03.{tmdb-9223372036854775808}.mkv", "reject", "media_identity_conflict"},
		{"zero tmdb", "Fixture.2020.S01E03.{tmdb-0}.mkv", "reject", "media_identity_conflict"},
		{"matching tag wrong title", "Other.2020.S01E03.{tmdb-100}.mkv", "hold", "media_title_unproven"},
		{"matching tag no year", "Fixture.S01E03.{tmdb-100}.mkv", "hold", "media_identity_unproven"},
		{"unknown tag no year", "Fixture.S01E03.{tvdb-100}.mkv", "hold", "media_identity_unproven"},
		{"imdb", "Fixture.2020.S01E03.[imdb-tt1234567].mkv", "hold", "external_identity_unverified"},
		{"imdb plus matching tmdb", "Fixture.2020.S01E03.{tmdb-100}.tt1234567.mkv", "hold", "external_identity_unverified"},
		{"imdb cannot mask wrong tmdb", "Fixture.2020.S01E03.{tmdb-999}.tt1234567.mkv", "reject", "media_identity_conflict"},
		{"ambiguous prefix years", "Fixture.2020.2021.S01E03.mkv", "hold", "ambiguous_media_year"},
		{"conflicting subtitle year", "Fixture.2020.S01E03.Finale.2021.mkv", "hold", "ambiguous_media_year"},
		{"subtitle year only", "Fixture.S01E03.Finale.2020.mkv", "hold", "media_identity_unproven"},
		{"subtitle date only", "Fixture.S01E03.2020.02.14.1080p.mkv", "hold", "media_identity_unproven"},
		{"special season", "Fixture.2020.S00E03.mkv", "hold", "identity_coverage_unresolved"},
		{"absolute", "[Team] Fixture.2020 - 25 [1080p].mkv", "hold", "identity_coverage_unresolved"},
		{"mixed absolute", "[Team] Fixture.2020 - 25 [1080p] S01E03.mkv", "hold", "identity_coverage_unresolved"},
		{"batch", "Fixture.2020.S01E01-E03.mkv", "hold", "identity_coverage_unresolved"},
		{"degenerate range", "Fixture.2020.S01E03-E03.mkv", "hold", "identity_coverage_unresolved"},
		{"list", "Fixture.2020.S01E01E03.mkv", "hold", "identity_coverage_unresolved"},
		{"multiple tokens", "Fixture.2020.S01E03.S01E04.mkv", "hold", "identity_coverage_unresolved"},
		{"repeated same token", "Fixture.2020.S01E03.S01E03.mkv", "hold", "identity_coverage_unresolved"},
		{"season pack", "Fixture.2020.S01.COMPLETE.mkv", "hold", "identity_coverage_unresolved"},
		{"sample", "Fixture.2020.S01E03.sample.mkv", "hold", "identity_coverage_unresolved"},
		{"movie", "Fixture.2020.mkv", "hold", "identity_coverage_unresolved"},
		{"unknown numbering", "Fixture.2020.E03.mkv", "hold", "identity_coverage_unresolved"},
		{"empty parsed title", "2020.S01E03.mkv", "hold", "media_identity_unproven"},
		// Current TV parsing does not separate a leading year from the title.
		{"leading year counterexample", "(2020).Fixture.S01E03.mkv", "hold", "media_title_unproven"},
		// A technical boundary can otherwise hide extra work-prefix text.
		{"cleaner counterexample", "Fixture.2020.1080p.Other.S01E03.mkv", "hold", "media_title_unproven"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := media.DecideAutomaticTVIdentity(tc.raw, "Fixture", "", 2020, 100)
			assertAutomaticTVIdentity(t, got, tc.state, tc.reason)
		})
	}
}

// Current TV parsing treats Season in this real work title as unresolved coverage.
func TestAutomaticTVIdentityParserCounterexample(t *testing.T) {
	raw := "The.Long.Season.2023.S01E03.1080p.WEB-DL.mkv"
	got := media.DecideAutomaticTVIdentity(raw, "漫长的季节", "The Long Season", 2023, 100)
	assertAutomaticTVIdentity(t, got, "hold", "identity_coverage_unresolved")
}

func TestAutomaticTVIdentityRequiresTrustedCatalogInputs(t *testing.T) {
	for _, tc := range []struct {
		name, title, original string
		year                  int
		seriesID              int64
	}{
		{"missing catalog", "", "", 2020, 100},
		{"punctuation only", "...", "---", 2020, 100},
		{"unsafe catalog", "Fixture\u200b", "", 2020, 100},
		{"control in catalog", "Fixture\n", "", 2020, 100},
		{"missing series", "Fixture", "", 2020, 0},
		{"negative series", "Fixture", "", 2020, -1},
		{"missing year", "Fixture", "", 0, 100},
		{"negative year", "Fixture", "", -1, 100},
		{"out of range year", "Fixture", "", 10000, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := media.DecideAutomaticTVIdentity("Fixture.2020.S01E03.mkv", tc.title, tc.original, tc.year, tc.seriesID)
			assertAutomaticTVIdentity(t, got, "hold", "media_identity_unproven")
		})
	}
	// Valid catalog dates outside the parser's supported year evidence remain hold.
	for _, raw := range []string{"Fixture.0001.S01E03.mkv", "Fixture.9999.S01E03.mkv"} {
		year := 1
		if strings.Contains(raw, "9999") {
			year = 9999
		}
		got := media.DecideAutomaticTVIdentity(raw, "Fixture", "", year, 100)
		assertAutomaticTVIdentity(t, got, "hold", "media_identity_unproven")
	}
}

func TestAutomaticTVIdentityUnsafeRawText(t *testing.T) {
	for _, raw := range []string{
		"", "https://tracker/Fixture.2020.S01E03", "Fixture.2020.S01E03.passkey=secret",
		"dir/Fixture.2020.S01E03.mkv", `dir\Fixture.2020.S01E03.mkv`,
		"Fixture.2020.S01E03\x00", "Fixture.2020.S01E03\n", "Fixture.2020.S01E03\t",
		"Fixture.2020.S01E03\u200b", "Fixture.2020.S01E03\u202e", "Fixture.2020.S01E03\u2028",
		"Fixture.2020.S01E03\u2215", "Fixture.2020.S01E03\uff0f", "Fixture.2020.S01E03\uff3c",
		"Fixture.2020.S01E03\u0301", "Fixture.2020.S01E03😊", "Fixture.2020.S01E03\xff",
		strings.Repeat("剧", 513) + ".2020.S01E03",
	} {
		t.Run(raw, func(t *testing.T) {
			got := media.DecideAutomaticTVIdentity(raw, "Fixture", "", 2020, 100)
			assertAutomaticTVIdentity(t, got, "hold", "unsafe_title")
		})
	}
}

func TestAutomaticTVIdentityTitleLengthBoundary(t *testing.T) {
	suffix := ".2020.S01E03"
	title := strings.Repeat("剧", 512-len(suffix))
	got := media.DecideAutomaticTVIdentity(title+suffix, title, "", 2020, 100)
	assertAutomaticTVIdentity(t, got, "eligible", "strict_title_year")
	got = media.DecideAutomaticTVIdentity(title+"剧"+suffix, title, "", 2020, 100)
	assertAutomaticTVIdentity(t, got, "hold", "unsafe_title")
}

func assertAutomaticTVIdentity(t *testing.T, got media.AutomaticTVIdentityDecision, state, reason string) {
	t.Helper()
	if got.State != state || got.Reason != reason || got.RuleVersion != "automatic-tv-identity-v1" ||
		media.AutomaticTVIdentityRuleVersion != "automatic-tv-identity-v1" || got.ParserVersion != media.EpisodeParserVersion {
		t.Fatalf("decision = %+v; want %s/%s with current rule and parser versions", got, state, reason)
	}
}
