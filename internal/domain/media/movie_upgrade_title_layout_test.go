package media

import "testing"

// Literal controls for supported title spelling, separate from the parser corpus.
func TestMovieUpgradeFilesBoundTitleLayouts(t *testing.T) {
	for name, tc := range map[string]struct {
		title string
		year  int
		files []TVTorrentFile
	}{
		"dot_parent":                {"Film.Name", 2000, []TVTorrentFile{{"Film.Name/Film.Name.2000.mkv", 4}}},
		"dot_sidecar":               {"Film.Name", 2000, []TVTorrentFile{{"Film.Name.2000.mkv", 4}, {"Film.Name.nfo", 0}}},
		"underscore_parent":         {"Film_Name", 2000, []TVTorrentFile{{"Film_Name/Film_Name.2000.mkv", 4}}},
		"underscore_sidecar":        {"Film_Name", 2000, []TVTorrentFile{{"Film_Name.2000.mkv", 4}, {"Film_Name.nfo", 0}}},
		"dot_alias_parent":          {"Film", 2000, []TVTorrentFile{{"Film.Name/Film.2000.mkv", 4}}},
		"underscore_alias_sidecar":  {"Film", 2000, []TVTorrentFile{{"Film.2000.mkv", 4}, {"Film_Name.nfo", 0}}},
		"ordinary_half_flat":        {"Half Nelson", 2006, []TVTorrentFile{{"Half.Nelson.2006.mkv", 4}}},
		"ordinary_half_year_parent": {"Half Nelson", 2006, []TVTorrentFile{{"Half.Nelson.2006/Half.Nelson.2006.mkv", 4}}},
	} {
		t.Run(name, func(t *testing.T) {
			target := movieUpgradeTargetFixture()
			target.OriginalTitle, target.Year, target.Aliases = tc.title, tc.year, nil
			if name == "dot_alias_parent" {
				target.Aliases = []string{"Film.Name"}
			}
			if name == "underscore_alias_sidecar" {
				target.Aliases = []string{"Film_Name"}
			}
			got, err := ValidateMovieUpgradeTorrentFiles(tc.files, target)
			if err != nil || got == nil || got.Path != tc.files[0].Path {
				t.Fatalf("bound title refused: %+v %v", got, err)
			}
		})
	}
}
