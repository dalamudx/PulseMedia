package torrent

import (
	"errors"
	"testing"
)

// Literal declarations enter through the real v1 decoder, including zero-byte files.
func TestMovieUpgradeProofBareAliasDeclarations(t *testing.T) {
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
				target := proofMovieTarget()
				target.OriginalTitle, target.Year = "Film", 2000
				target.Identities[0].ExternalID = "1"
				target.Aliases = []string{alias}
				raw := movieMulti(alias, movieFile("Film.2000.mkv", 4))
				if sidecar {
					raw = movieMulti("Film", movieFile("Film.2000.mkv", 4), movieFile(alias+".nfo", 0))
				}
				got, err := InspectMovieUpgradeMetainfo(raw, target)
				if got != nil || !errors.Is(err, ErrMovieUpgradeMetainfoReview) {
					t.Fatalf("contradictory raw alias minted proof: %v", err)
				}
			})
		}
	}
}
