package media

import "testing"

func TestMovieUpgradeFilesUnexplainedTailAndParent(t *testing.T) {
	for _, name := range []string{
		"Fight.Club.1999.Inception.mkv",
		"Fight.Club.1999.UnknownCoverage.mkv",
		"Fight.Club.1999.1080p.Inception.mkv",
		"Fight.Club.1999.1080p-GROUP.mkv",
		"Fight.Club.1999.Inception/Fight.Club.1999.mkv",
		"Fight.Club.1999.S01E01/Fight.Club.1999.mkv",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ValidateMovieUpgradeTorrentFiles([]TVTorrentFile{{name, 4}}, movieUpgradeTargetFixture()); got != nil || err != ErrMovieUpgradeFilesReview {
				t.Fatal("unexplained declaration accepted", got, err)
			}
		})
	}
	for _, name := range []string{"Fight.Club.1999.1080p.BluRay.x264.mkv", "Fight Club (1999).mkv"} {
		t.Run(name, func(t *testing.T) {
			if got, err := ValidateMovieUpgradeTorrentFiles([]TVTorrentFile{{name, 4}}, movieUpgradeTargetFixture()); err != nil || got == nil {
				t.Fatal("supported layout rejected", err)
			}
		})
	}
}
func TestMovieUpgradeFilesControlledAliasCannotHideCoverage(t *testing.T) {
	for _, alias := range []string{"Fight Club S01E01", "Fight Club Season 1", "Fight Club OVA", "Fight Club part1"} {
		t.Run(alias, func(t *testing.T) {
			target := movieUpgradeTargetFixture()
			target.Aliases = []string{alias}
			if got, err := ValidateMovieUpgradeTorrentFiles([]TVTorrentFile{{alias + "/Fight.Club.1999.mkv", 4}}, target); got != nil || err != ErrMovieUpgradeFilesReview {
				t.Fatal("alias hid coverage", got, err)
			}
		})
	}
}
func TestMovieUpgradeFilesUnicodeAliasesCollide(t *testing.T) {
	target := movieUpgradeTargetFixture()
	target.Aliases = []string{"Straße", "STRASSE"}
	files := []TVTorrentFile{{"Fight.Club.1999.mkv", 4}, {"Straße.nfo", 0}, {"STRASSE.nfo", 0}}
	if got, err := ValidateMovieUpgradeTorrentFiles(files, target); got != nil || err != ErrMovieUpgradeFilesReview {
		t.Fatal("Unicode path aliases accepted", got, err)
	}
	target.Aliases = []string{"Café"}
	files = []TVTorrentFile{{"Fight.Club.1999.mkv", 4}, {"Cafe\u0301.nfo", 0}}
	if got, err := ValidateMovieUpgradeTorrentFiles(files, target); got != nil || err != ErrMovieUpgradeFilesReview {
		t.Fatal("decomposed path accepted", got, err)
	}
}
