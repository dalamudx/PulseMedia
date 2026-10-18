package media

import (
	"regexp"
	"strings"
)

// Unsupported source labels are title delimiters only in this explicit release
// tail. This does not make DVD, camera or screener labels supported quality facts.
var unsupportedReleaseTitleTailRegex = regexp.MustCompile(`\s+(?:CAM|SCR|DVDR)(?:\s+(?i:xvid|divx))?-[A-Za-z0-9][A-Za-z0-9_-]{1,39}$`)

func stripUnsupportedReleaseTitleTail(title string) string {
	span := unsupportedReleaseTitleTailRegex.FindStringIndex(title)
	if span == nil || len(strings.Fields(title[:span[0]])) < 2 {
		return title
	}
	return title[:span[0]]
}
