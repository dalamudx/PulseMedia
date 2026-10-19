package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestTMDBPosterPathTrustedProviderSizes(t *testing.T) {
	for _, size := range []string{"w92", "w154", "w185", "w342", "w500", "w780", "original"} {
		for _, path := range []string{"/aB09_-z.jpg", "/poster.jpeg", "/poster.png", "/poster.webp"} {
			raw := "https://image.tmdb.org/t/p/" + size + path
			if got := media.TMDBPosterPath(raw); got != path {
				t.Errorf("TMDBPosterPath(%q) = %q; want %q", raw, got, path)
			}
			if got := media.TMDBPosterURL(path); got != "https://image.tmdb.org/t/p/w342"+path {
				t.Errorf("TMDBPosterURL(%q) = %q", path, got)
			}
		}
	}
}

func TestTMDBPosterPathRejectsUntrustedURLs(t *testing.T) {
	for _, raw := range []string{
		"", "/poster.jpg", "//image.tmdb.org/t/p/w342/poster.jpg",
		"http://image.tmdb.org/t/p/w342/poster.jpg",
		"https://IMAGE.tmdb.org/t/p/w342/poster.jpg",
		"https://image.tmdb.org.evil.test/t/p/w342/poster.jpg",
		"https://image.tmdb.org./t/p/w342/poster.jpg",
		"https://evil.test/t/p/w342/poster.jpg",
		"https://image.tmdb.org:443/t/p/w342/poster.jpg",
		"https://image.tmdb.org:80/t/p/w342/poster.jpg",
		"https://user" + "@" + "image.tmdb.org/t/p/w342/poster.jpg",
		"https://image.tmdb.org@evil.test/t/p/w342/poster.jpg",
		"https://image.tmdb.org/t/p/w342/poster.jpg?secret=x",
		"https://image.tmdb.org/t/p/w342/poster.jpg?",
		"https://image.tmdb.org/t/p/w342/poster.jpg#fragment",
		"https://image.tmdb.org/t/p/w343/poster.jpg",
		"https://image.tmdb.org/t/p/w342/../poster.jpg",
		"https://image.tmdb.org/t/p/w342//poster.jpg",
		"https://image.tmdb.org/t/p/w342/nested/poster.jpg",
		"https://image.tmdb.org/t/p/w342/%70oster.jpg",
		"https://image.tmdb.org/t/p/w342/poster%2Ejpg",
		"https://image.tmdb.org/t/p/w342/%2e%2e%2fposter.jpg",
		"https://image.tmdb.org/t/p/w342/%252e%252e.jpg",
		"https://image.tmdb.org/t/p/w342/poster%3Fsecret.jpg",
		"https://image.tmdb.org/t/p/w342/poster%23fragment.jpg",
		"https://image.tmdb.org/t/p/w342/poster\\escape.jpg",
		"https://image.tmdb.org/t/p/w342/poster.svg",
		"https://image.tmdb.org/t/p/w342/poster.JPG",
		"https://image.tmdb.org/t/p/w342/.jpg",
		"https://image.tmdb.org/t/p/w342/poster.jpg/",
		" https://image.tmdb.org/t/p/w342/poster.jpg",
		"https://image.tmdb.org/t/p/w342/poster.jpg\n",
	} {
		t.Run(raw, func(t *testing.T) {
			if got := media.TMDBPosterPath(raw); got != "" {
				t.Errorf("accepted untrusted URL %q as %q", raw, got)
			}
		})
	}
}

func TestTMDBPosterURLRejectsNoncanonicalPaths(t *testing.T) {
	for _, path := range []string{"", "poster.jpg", "//poster.jpg", "/../poster.jpg", "/a/b.jpg", "/poster.jpg?x", "/poster.jpg#x", "/%70oster.jpg", "/a%2fb.jpg", "/a\\b.jpg", "/poster.svg", "/poster.JPG", "/.jpg", "/a.b.jpg", "/ poster.jpg", "/poster.jpg\n", "https://image.tmdb.org/t/p/w342/poster.jpg"} {
		if got := media.TMDBPosterURL(path); got != "" {
			t.Errorf("accepted noncanonical path %q as %q", path, got)
		}
	}
}
