package media

import (
	"errors"
	"testing"
)

// Self-authored negative controls, not independent golden parser samples.
func TestMovieUpgradeFilesBareAliasDeclarations(t *testing.T) {
	for name, alias := range map[string]string{
		"foreign_tmdb": "Film {tmdb-2}",
		"foreign_imdb": "Film tt9999999",
		"wrong_year":   "Film 2001",
		"collection":   "Film Trilogy",
		"all_seasons":  "Film All Seasons (1-8)",
	} {
		for _, sidecar := range []bool{false, true} {
			layout := "parent"
			if sidecar {
				layout = "zero_sidecar"
			}
			t.Run(name+"/"+layout, func(t *testing.T) {
				target := movieUpgradeTargetFixture()
				target.OriginalTitle, target.Year = "Film", 2000
				target.Identities[0].ExternalID = "1"
				target.Aliases = []string{alias}
				files := []TVTorrentFile{{Path: alias + "/Film.2000.mkv", Size: 4}}
				if sidecar {
					files = []TVTorrentFile{{Path: "Film/Film.2000.mkv", Size: 4}, {Path: "Film/" + alias + ".nfo", Size: 0}}
				}
				got, err := ValidateMovieUpgradeTorrentFiles(files, target)
				if got != nil || !errors.Is(err, ErrMovieUpgradeFilesReview) {
					t.Fatalf("contradictory alias accepted: %+v %v", got, err)
				}
			})
		}
	}
}
