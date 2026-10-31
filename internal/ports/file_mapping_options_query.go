package ports

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Valid checks transport-independent bounds; cursors are verified by the reader.
func (q FileMappingOptionsQuery) Valid() bool {
	if len(q.Cursor) > 512 || len(q.MediaCursor) > 512 || len(q.MediaQuery) > 480 || utf8.RuneCountInString(q.MediaQuery) > 120 || !utf8.ValidString(q.MediaQuery) || strings.TrimSpace(q.MediaQuery) != q.MediaQuery || q.TMDBID < 0 {
		return false
	}
	if q.MediaType != "" && q.MediaType != "movie" && q.MediaType != "tv" {
		return false
	}
	return strings.IndexFunc(q.MediaQuery, unicode.IsControl) == -1
}
