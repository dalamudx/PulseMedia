package media

import "testing"

func TestMovieUpgradeFilesDirectoryAliasesRequireReview(t *testing.T) {
	for name, files := range map[string][]TVTorrentFile{
		"root_case":     {{"Fight Club/Fight.Club.1999.mkv", 4}, {"FIGHT CLUB/readme.txt", 0}},
		"subtitle_case": {{"Fight Club/Fight.Club.1999.mkv", 4}, {"Fight Club/Subs/en.srt", 0}, {"Fight Club/subs/zh.srt", 0}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ValidateMovieUpgradeTorrentFiles(files, movieUpgradeTargetFixture()); got != nil || err != ErrMovieUpgradeFilesReview {
				t.Fatal("directory aliases accepted", got, err)
			}
		})
	}
}
