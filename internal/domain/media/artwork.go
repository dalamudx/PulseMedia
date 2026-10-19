package media

import (
	"net/url"
	"regexp"
	"strings"
)

var tmdbPosterPath = regexp.MustCompile(`^/[A-Za-z0-9_-]+\.(jpg|jpeg|png|webp)$`)

// TMDBPosterPath accepts only provider image URLs, never arbitrary fetch targets.
// Persist the provider's opaque path separately from frozen media rows.
func TMDBPosterPath(raw string) string {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "image.tmdb.org" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" {
		return ""
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 5 || parts[1] != "t" || parts[2] != "p" {
		return ""
	}
	switch parts[3] {
	case "w92", "w154", "w185", "w342", "w500", "w780", "original":
	default:
		return ""
	}
	path := "/" + parts[4]
	if !tmdbPosterPath.MatchString(path) {
		return ""
	}
	return path
}

func TMDBPosterURL(path string) string {
	if !tmdbPosterPath.MatchString(path) {
		return ""
	}
	return "https://image.tmdb.org/t/p/w342" + path
}
