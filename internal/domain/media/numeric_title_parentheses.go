package media

import (
	"regexp"
	"strings"
)

var numericMovieTitlePrefixRegex = regexp.MustCompile(`^\([0-9]{1,3}\)(?:\s|$)`)

// A complete leading short numeric title is not an unmatched release-year
// bracket. Preserve only that paired prefix; do not repair partial decorations
// or change work-year/coverage recognition.
func trimMovieTitleEdges(title string) string {
	title = strings.TrimSpace(title)
	if at := numericMovieTitlePrefixRegex.FindStringIndex(title); at != nil {
		head := strings.TrimSpace(title[:at[1]])
		tail := strings.Trim(title[at[1]:], " -_()[]{}")
		if tail == "" {
			return head
		}
		return head + " " + tail
	}
	return strings.Trim(title, " -_()[]{}")
}
