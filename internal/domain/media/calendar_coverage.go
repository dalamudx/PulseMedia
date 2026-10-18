package media

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Bounded YYYY-MM-DD, YYYY.MM.DD, YYYY_MM_DD, YYYY MM DD and YYYYMMDD
// stamps are calendar coverage evidence, never a work's release year.
var filenameCalendarStamp = regexp.MustCompile(`(?:19|20)[0-9]{2}(?:[ ._-][0-9]{2}[ ._-][0-9]{2}|[0-9]{4})`)

// English calendar stamps require an explicit day, month and year. A month
// word in a work title (November.2004) is not calendar coverage evidence.
const filenameEnglishMonth = `(?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|Aug(?:ust)?|Sep(?:tember|t)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)`

var filenameDayMonthStamp = regexp.MustCompile(`(?i)([0-9]{1,2})(st|nd|rd|th)?[ ._-]+(` + filenameEnglishMonth + `)[ ._-]+((?:19|20)[0-9]{2})`)
var filenameMonthDayStamp = regexp.MustCompile(`(?i)(` + filenameEnglishMonth + `)[ ._-]+([0-9]{1,2})(st|nd|rd|th)?(?:,[ ._-]*|[ ._-]+)((?:19|20)[0-9]{2})`)

func calendarFilenameSpans(s string) [][]int {
	// External identities are not dates; preserve byte positions for raw-name use.
	analysis := tmdbIDRegex.ReplaceAllStringFunc(s, func(v string) string { return strings.Repeat(" ", len(v)) })
	analysis = imdbIDRegex.ReplaceAllStringFunc(analysis, func(v string) string { return strings.Repeat(" ", len(v)) })
	var spans [][]int
	boundary := func(b byte) bool { return strings.ContainsRune(" ._-()[]{}", rune(b)) }
	for _, pattern := range []*regexp.Regexp{filenameCalendarStamp, filenameDayMonthStamp, filenameMonthDayStamp} {
		for _, p := range pattern.FindAllStringIndex(analysis, -1) {
			if (p[0] == 0 || boundary(analysis[p[0]-1])) && (p[1] == len(analysis) || boundary(analysis[p[1]])) {
				spans = append(spans, p)
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	return spans
}

func maskFilenameCalendarStamps(s string) string {
	spans := calendarFilenameSpans(s)
	for i := len(spans) - 1; i >= 0; i-- {
		p := spans[i]
		s = s[:p[0]] + strings.Repeat(" ", p[1]-p[0]) + s[p[1]:]
	}
	return s
}

func validCalendarStamp(s string) bool {
	if match := filenameDayMonthStamp.FindStringSubmatch(s); len(match) > 0 && match[0] == s {
		return validEnglishCalendarStamp(match[1], match[2], match[3], match[4])
	}
	if match := filenameMonthDayStamp.FindStringSubmatch(s); len(match) > 0 && match[0] == s {
		return validEnglishCalendarStamp(match[2], match[3], match[1], match[4])
	}
	if len(s) == 10 && s[4] != s[7] {
		return false
	}
	value := strings.NewReplacer(".", "", "_", "", "-", "", " ", "").Replace(s)
	_, err := time.Parse("20060102", value)
	return err == nil
}

func validEnglishCalendarStamp(day, suffix, month, year string) bool {
	d, err := strconv.Atoi(day)
	if err != nil || d < 1 || d > 31 {
		return false
	}
	if suffix != "" {
		wanted := "th"
		if d%100 < 11 || d%100 > 13 {
			switch d % 10 {
			case 1:
				wanted = "st"
			case 2:
				wanted = "nd"
			case 3:
				wanted = "rd"
			}
		}
		if strings.ToLower(suffix) != wanted {
			return false
		}
	}
	var m time.Month
	switch strings.ToLower(month[:3]) {
	case "jan":
		m = time.January
	case "feb":
		m = time.February
	case "mar":
		m = time.March
	case "apr":
		m = time.April
	case "may":
		m = time.May
	case "jun":
		m = time.June
	case "jul":
		m = time.July
	case "aug":
		m = time.August
	case "sep":
		m = time.September
	case "oct":
		m = time.October
	case "nov":
		m = time.November
	case "dec":
		m = time.December
	default:
		return false
	}
	y, err := strconv.Atoi(year)
	if err != nil {
		return false
	}
	date := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return date.Year() == y && date.Month() == m && date.Day() == d
}

// Strip only leading, valid, bounded dates for the existing anchored suffix
// guard. Replacing them with spaces could conceal an additional .02 or -E03.
func skipLeadingCalendarStamps(s string) string {
	for {
		spans := calendarFilenameSpans(s)
		if len(spans) == 0 {
			return s
		}
		p := spans[0]
		if strings.Trim(s[:p[0]], " ._-") != "" || !validCalendarStamp(s[p[0]:p[1]]) {
			return s
		}
		s = s[p[1]:]
	}
}
