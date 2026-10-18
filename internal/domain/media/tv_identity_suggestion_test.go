package media

import "testing"

func TestTVIdentitySuggestionNeverUsesBareMatchingIDAsAuthority(t *testing.T) {
	for _, c := range []struct {
		raw, title       string
		year             int
		decision, reason string
	}{{"Fixture.S01E01.Finale.2020.1080p.mkv", "Fixture", 2020, "hold", "media_identity_unproven"}, {"Doctor.Who.2005.8x11.720p.HDTV.x264", "Doctor Who", 2005, "suggested", "exact_title_year_review"}, {"Fixture.2020.S01E01.1080p.mkv", "Fixture", 2020, "suggested", "exact_title_year_review"}, {"Other.2020.S01E01.1080p.{tmdb-100}.mkv", "Fixture", 2020, "hold", "media_title_unproven"}, {"Fixture.2021.S01E01.1080p.{tmdb-100}.mkv", "Fixture", 2020, "reject", "media_year_conflict"}, {"Fixture.2020.S01E01.{tmdb-100}.{tmdb-999}.mkv", "Fixture", 2020, "reject", "media_identity_conflict"}, {"Fixture.2020.S01E01.{tmdb-9223372036854775808}.mkv", "Fixture", 2020, "reject", "media_identity_conflict"}, {"Fixture.S01E01.1080p.{tmdb-100}.mkv", "Fixture", 2020, "hold", "media_identity_unproven"}, {"Fixture.2020.S01E01.S01E03.1080p.mkv", "Fixture", 2020, "hold", "identity_coverage_unresolved"}, {"Fixture.2020.S01E01.sample.mkv", "Fixture", 2020, "hold", "identity_coverage_unresolved"}, {"Fixture.2020.S01E01.{imdb-tt1234567}.mkv", "Fixture", 2020, "hold", "external_identity_unverified"}} {
		d, r := SuggestTVIdentity(c.raw, c.title, "", c.year, 100)
		if d != c.decision || r != c.reason {
			t.Fatal(c.raw, d, r, "want", c.decision, c.reason)
		}
	}
}
