package quality

import (
	"regexp"
	"strings"
)

// Only complete paired identity decorations or delimited bare IMDb trailers
// are excluded from the group view. Adjacent separators are decoration, not
// part of the release group. Unknown/malformed tags remain visible; this does
// not validate an external ID, rewrite original evidence, or assign a group.
var releaseIdentityTagRegex = regexp.MustCompile(`(?i)[ ._-]*(?:\{(?:tmdb-[0-9]+|(?:imdb-)?tt[0-9]{7,10})\}|\[(?:tmdb-[0-9]+|(?:imdb-)?tt[0-9]{7,10})\]|\((?:tmdb-[0-9]+|(?:imdb-)?tt[0-9]{7,10})\))`)
var bareReleaseIdentityTrailerRegex = regexp.MustCompile(`(?i)[ ._-]+(?:imdb-)?tt[0-9]{7,10}((?:\[[^\]]*\])*)$`)

// WithoutReleaseIdentityTags returns a temporary group-parsing view. Callers
// retain the original name and persisted attributes as source evidence.
func WithoutReleaseIdentityTags(name string) string {
	stripped := releaseIdentityTagRegex.ReplaceAllString(name, "")
	// Bracket suffixes are retained verbatim, including unknown tags. Only
	// negative comparisons may use an unreviewed suffix; it cannot certify a group.
	stripped = bareReleaseIdentityTrailerRegex.ReplaceAllString(stripped, "$1")
	if stripped == name {
		return name
	}
	return strings.TrimRight(stripped, " ._-")
}
