package media

import (
	"regexp"
	"strings"
	"unicode"
)

var automaticPack = regexp.MustCompile(`(?i)\b(complete|collection|pack|duology|trilogy)\b|\bs\d{1,2}(e\d{1,3})?|\b\d+x\d{1,3}`)

// MatchesAutomaticMovie is deliberately conservative. Title/year fallback must
// be exact; a conflicting explicit ID or uncertain/multi-unit coverage is hold.
func MatchesAutomaticMovie(raw, title string, year int, identities map[string]string) bool {
	if strings.ContainsAny(raw, "/\\") || automaticPack.MatchString(raw) {
		return false
	}
	coverage := RecognizeRelease(raw)
	if coverage.MediaType != TypeMovie || coverage.RequiresReview || len(coverage.UnitKeys) != 1 || coverage.UnitKeys[0] != FormatMovieUnitKey() {
		return false
	}
	rec := Recognize(raw)
	if rec.RequiresReview {
		return false
	}
	if rec.Year > 0 && year > 0 && rec.Year != year {
		return false
	}
	explicit := false
	for _, m := range tmdbIDRegex.FindAllStringSubmatch(raw, -1) {
		explicit = true
		if identities["tmdb.movie"] != m[1] {
			return false
		}
	}
	for _, m := range imdbIDRegex.FindAllStringSubmatch(raw, -1) {
		explicit = true
		if !strings.EqualFold(identities["imdb"], m[2]) {
			return false
		}
	}
	if explicit {
		return true
	}
	return year > 0 && rec.Year == year && normalizeMovieTitle(rec.Title) == normalizeMovieTitle(title)
}
func normalizeMovieTitle(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}
