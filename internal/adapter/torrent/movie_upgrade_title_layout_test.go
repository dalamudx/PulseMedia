package torrent

import "testing"

func TestMovieUpgradeProofBoundTitleLayouts(t *testing.T) {
	for name, tc := range map[string]struct {
		title string
		year  int
		raw   []byte
	}{
		"dot_parent":                {"Film.Name", 2000, movieMulti("Film.Name", movieFile("Film.Name.2000.mkv", 4))},
		"dot_sidecar":               {"Film.Name", 2000, movieMulti("Film.Name.2000", movieFile("Film.Name.2000.mkv", 4), movieFile("Film.Name.nfo", 0))},
		"underscore_parent":         {"Film_Name", 2000, movieMulti("Film_Name", movieFile("Film_Name.2000.mkv", 4))},
		"underscore_sidecar":        {"Film_Name", 2000, movieMulti("Film_Name.2000", movieFile("Film_Name.2000.mkv", 4), movieFile("Film_Name.nfo", 0))},
		"dot_alias_parent":          {"Film", 2000, movieMulti("Film.Name", movieFile("Film.2000.mkv", 4))},
		"underscore_alias_sidecar":  {"Film", 2000, movieMulti("Film.2000", movieFile("Film.2000.mkv", 4), movieFile("Film_Name.nfo", 0))},
		"ordinary_half_flat":        {"Half Nelson", 2006, []byte("d4:infod6:lengthi4e4:name" + movieBS("Half.Nelson.2006.mkv") + "12:piece lengthi4e6:pieces20:00000000000000000000ee")},
		"ordinary_half_year_parent": {"Half Nelson", 2006, movieMulti("Half.Nelson.2006", movieFile("Half.Nelson.2006.mkv", 4))},
	} {
		t.Run(name, func(t *testing.T) {
			target := proofMovieTarget()
			target.OriginalTitle, target.Year, target.Aliases = tc.title, tc.year, nil
			if name == "dot_alias_parent" {
				target.Aliases = []string{"Film.Name"}
			}
			if name == "underscore_alias_sidecar" {
				target.Aliases = []string{"Film_Name"}
			}
			p, err := InspectMovieUpgradeMetainfo(tc.raw, target)
			if err != nil || !p.ValidateBinding() {
				t.Fatalf("bound raw title refused: %v", err)
			}
		})
	}
}
