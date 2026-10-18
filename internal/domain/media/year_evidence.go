package media

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"regexp"
	"strconv"
	"strings"
)

var technicalRipYearBoundaryRegex = regexp.MustCompile(`(?i)\b(?:dvdrip|bdrip|brrip|hdrip|bdremux)\b`)

// Filename dates are evidence, not a ranking heuristic. Distinct possible years cannot be resolved by first/last number.
func recognizeYear(s string) (int, bool) {
	s = quality.WithoutReleaseIdentityTags(s)
	// Numeric groups after technical evidence do not date the work; ordinary
	// Movie-1999 and conflicting title years retain the existing ambiguity gate.
	if at := quality.ReleaseGroupDelimiter(s); at >= 0 {
		tail := stripKnownRelayTags(s[at+1:])
		if len(tail) == 4 && yearRegex.MatchString(tail) && noiseRegex.MatchString(s[:at]) {
			s = s[:at]
		}
	}
	s = tmdbIDRegex.ReplaceAllString(s, "")
	s = imdbIDRegex.ReplaceAllString(s, "")
	// Air dates (including malformed calendar-shaped stamps) never supply a work year.
	s = maskFilenameCalendarStamps(s)
	// A purely numeric work title before its first explicit season/episode token
	// is not a release date (for example the series named 1923). Do not guess
	// between multiple numbers: those still use the ambiguity gate below.
	numericTitle := 0
	index := standardEpRegex.FindStringIndex(s)
	if alternate := altEpRegex.FindStringIndex(s); alternate != nil && (index == nil || alternate[0] < index[0]) {
		index = alternate
	}
	if index != nil {
		prefix := strings.Trim(s[:index[0]], " ._-[]()")
		if len(prefix) == 4 {
			if value, e := strconv.Atoi(prefix); e == nil {
				numericTitle = value
			}
		}
	}
	// A sole numeric work name before release-quality evidence does not
	// independently establish a date. Keep every candidate in the set below
	// first so a second possible year still fails closed as ambiguous.
	numericMovieTitle := 0
	if index == nil {
		if at := noiseRegex.FindStringIndex(s); at != nil {
			prefix := strings.Trim(s[:at[0]], " ._-[]()")
			if len(prefix) == 4 {
				numericMovieTitle, _ = strconv.Atoi(prefix)
			}
		}
	}
	normalized := strings.NewReplacer(".", " ", "_", " ").Replace(s)
	years := map[int]bool{}
	value := 0
	earliestYear := len(normalized)
	for _, m := range yearRegex.FindAllStringSubmatchIndex(normalized, -1) {
		y, e := strconv.Atoi(normalized[m[2]:m[3]])
		if e == nil && y >= 1900 && y <= 2099 && y != numericTitle {
			years[y] = true
			value = y
			if m[2] < earliestYear {
				earliestYear = m[2]
			}
		}
	}
	if len(years) > 1 {
		return 0, true
	}
	if value != 0 && value == numericMovieTitle {
		return 0, false
	}
	// Keep conflicting years ambiguous; a sole year in a technical rip tail
	// cannot independently date the work. An explicit earlier work year stays.
	if at := technicalRipYearBoundaryRegex.FindStringIndex(normalized); at != nil && earliestYear >= at[0] {
		return 0, false
	}
	return value, false
}

// Remove only a trailing, already unambiguous release-year token from the
// series prefix. Numbers inside the work title and unknown dates stay intact.
func stripSeriesReleaseYear(title string, year int) string {
	if year == 0 {
		return title
	}
	spans := yearRegex.FindAllStringIndex(title, -1)
	if len(spans) == 0 {
		return title
	}
	last := spans[len(spans)-1]
	if last[0] == 0 || last[1] != len(title) || title[last[0]:last[1]] != strconv.Itoa(year) {
		return title
	}
	return cleanTitle(title[:last[0]])
}
