package media

import (
	"regexp"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// A single uppercase letter is a publisher claim only at a tight technical
// suffix of a dated release. Bare title dashes and parent-path facts do not count.
var singleLetterCodecTail = regexp.MustCompile(`(?i)(?:^|[ ._])(?:x264|h264|x265|h265|hevc|av1)-[a-z]$`)

func singleLetterTechnicalGroup(name, candidate string, profile quality.Profile) bool {
	if len(candidate) != 1 || candidate[0] < 'A' || candidate[0] > 'Z' || profile.Resolution == "" || profile.Codec == "" || strings.ContainsAny(name, `/\`) {
		return false
	}
	year, ambiguous := recognizeYear(name)
	return !ambiguous && (year > 0 || (len(standardEpRegex.FindAllString(name, -1)) == 1 && !strings.ContainsAny(name, "{}[]") && len(calendarFilenameSpans(name)) == 0)) && singleLetterCodecTail.MatchString(stripKnownRelayTags(name))
}

// Keep an entire ordinary title's balanced final decoration. This does not
// restore metadata cut from a release title or repair malformed punctuation.
func trimOrdinaryPairedTitle(title, raw string) string {
	year, ambiguous := recognizeYear(raw)
	if year != 0 || ambiguous || title != strings.TrimSpace(normalizeTitleSeparators(stripReleaseTitleDecorations(raw))) {
		return trimMovieTitleEdges(title)
	}
	title = strings.TrimSpace(title)
	if len(title) == 0 || !strings.ContainsAny(title[len(title)-1:], ")]}") {
		return trimMovieTitleEdges(title)
	}
	var stack []rune
	for _, r := range title {
		switch r {
		case '(', '[', '{':
			stack = append(stack, r)
		case ')', ']', '}':
			if len(stack) == 0 {
				return trimMovieTitleEdges(title)
			}
			open := stack[len(stack)-1]
			if (open == '(' && r != ')') || (open == '[' && r != ']') || (open == '{' && r != '}') {
				return trimMovieTitleEdges(title)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) != 0 {
		return trimMovieTitleEdges(title)
	}
	return strings.Trim(title, " -_")
}
