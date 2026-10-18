package media

import "testing"

func TestAutomaticMovieMatchingConservative(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		year int
		want bool
	}{
		{"Fight.Club.1999.1080p.BluRay.mkv", 1999, true},
		{"Fight Club 1999 2160p", 1999, true},
		{"Fight.Club.1998.1080p.mkv", 1999, false},
		{"Fight.Club.1999.1080p.mkv", 0, false},
		{"Another.Club.1999.1080p.mkv", 1999, false},
		{"Fight.Club.1999.Complete.Collection", 1999, false},
		{"Fight.Club.S01E02.1999.1080p", 1999, false},
		{"path/Fight.Club.1999.mkv", 1999, false},
		{"Fight.Club.1999.{tmdb-550}.mkv", 1999, true},
		{"Fight.Club.1999.{tmdb-551}.mkv", 1999, false},
		{"Fight.Club.1999.{tmdb-551}", 1999, false},
		{"Fight.Club.1999.{tmdb-550}", 1999, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := MatchesAutomaticMovie(tc.raw, "Fight Club", tc.year, map[string]string{"tmdb.movie": "550"}); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
}
