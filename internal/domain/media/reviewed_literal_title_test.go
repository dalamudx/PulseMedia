package media_test

import "testing"

func TestLiteralReviewTitleCannotRewriteIndependentLabel(t *testing.T) {
	for _, tc := range []struct{ input, label, literal string }{
		{"A.Movie.2001", "A Movie", "Another Movie"},
		{"A.Movie.2001", "A Movie", "a movie"},
		{"A.Movie.2001", "A Movie", "A Movie"},
		{"A-Movie.2001", "A Movie", "A-Movie"},
		{"A.Movie.2001", "A Movie", "A movie 2001"},
	} {
		if _, err := validatedLiteralReviewTitle(tc.input, tc.label, tc.literal); err == nil {
			t.Errorf("invalid literal rewrite accepted: %+v", tc)
		}
	}
	got, err := validatedLiteralReviewTitle("A.movie.2001", "A Movie", "A movie")
	if err != nil || got != "A movie" {
		t.Fatalf("corroborated literal casing rejected: %q %v", got, err)
	}
	got, err = validatedLiteralReviewTitle("A.Movie.2001", "A Movie", "")
	if err != nil || got != "A Movie" {
		t.Fatalf("old exact title contract changed: %q %v", got, err)
	}
}
