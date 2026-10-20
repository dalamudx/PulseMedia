package media

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// WatchlistTitleKey preserves distinguishing punctuation and symbols while
// normalizing only Unicode composition, case and whitespace.
func WatchlistTitleKey(title string) string {
	if !utf8.ValidString(title) || len(title) > 512 {
		return ""
	}
	for _, r := range title {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return ""
		}
	}
	return strings.ToLower(norm.NFC.String(strings.Join(strings.Fields(title), " ")))
}
