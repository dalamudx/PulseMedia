package media

import (
	"regexp"
	"strconv"
	"strings"
)

// A publisher-bound literal interval is evidence of unresolved coverage, not
// authorization for either endpoint. Keep this separate from canonical SxxExx
// and from single absolute-number hints that require a published mapping.
var publisherEpisodeIntervalRegex = regexp.MustCompile(`(?i)(?:^|[/\\])[ ._-]*\[([^\[\]/\\]{1,64})\][ ._-]*([^/\\\r\n]+?)[ ._-]+(?:-[ ._-]+)?\(?([0-9]{1,4})(?:[ \t]*-[ \t]*|[ \t]*[~&][ \t]*)([0-9]{1,4})(?:[ ._\[\]()]|$)`)
var publisherRangeEpisodeWordSuffix = regexp.MustCompile(`(?i)\b(?:episodes?|episodios?|eps?\.?)\s*$`)
var publisherRangeBatchPrefix = regexp.MustCompile(`(?i)[ ._]+-[ ._]*batch$`)
var publisherRangeBatchSuffix = regexp.MustCompile(`(?i)^[ ._-]*(?:batch|\(batch\)|\[batch\])(?:[ ._]*\[[^\[\]\r\n]*\])*[ ._]*(?:\.(?:mkv|mp4|avi|m4v|mov|wmv|ts|m2ts|iso))?$`)
var publisherRangeTrailingYearDate = regexp.MustCompile(`\b[0-9]{1,2}-[0-9]{1,2}-(?:19|20)[0-9]{2}\b`)
var publisherRangeLeadingYearDate = regexp.MustCompile(`\b(?:19|20)[0-9]{2}[ ._-]+(?:0?[1-9]|1[0-2])[ ._-]+(?:0?[1-9]|[12][0-9]|3[01])\b`)

type publisherEpisodeInterval struct {
	publisher string
	title     string
}

func publisherEpisodeRangeHint(name string) *publisherEpisodeInterval {
	for _, match := range publisherEpisodeIntervalRegex.FindAllStringSubmatchIndex(name, -1) {
		title := name[match[4]:match[5]]
		if publisherRangeEpisodeWordSuffix.MatchString(title) {
			continue // Existing explicit-episode coverage checks own these words.
		}
		suffix := match[9]
		if name[match[6]-1] == '(' {
			if suffix >= len(name) || name[suffix] != ')' {
				continue // Do not repair an incomplete parenthesized declaration.
			}
			suffix++
		}
		separator := name[match[7]:match[8]]
		if strings.Contains(separator, "-") && separator != "-" && !publisherRangeBatchSuffix.MatchString(name[suffix:]) {
			// A numeric title followed by an absolute episode is ambiguous.
			// Only an independent, complete Batch suffix resolves that grammar.
			continue
		}
		title = publisherRangeBatchPrefix.ReplaceAllString(title, "")
		start, _ := strconv.Atoi(name[match[6]:match[7]])
		end, _ := strconv.Atoi(name[match[8]:match[9]])
		if start >= 1900 && end >= 1900 {
			continue // Preserve existing movie-year ambiguity handling.
		}
		insideDate := false
		dates := append(publisherRangeLeadingYearDate.FindAllStringIndex(name, -1), publisherRangeTrailingYearDate.FindAllStringIndex(name, -1)...)
		for _, date := range dates {
			if date[0] <= match[6] && date[1] >= match[9] {
				insideDate = true
				break
			}
		}
		if insideDate {
			continue
		}
		return &publisherEpisodeInterval{publisher: name[match[2]:match[3]], title: title}
	}
	return nil
}
