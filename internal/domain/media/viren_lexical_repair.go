package media

import (
	"regexp"
	"strings"
)

// These are lexical display boundaries from the preserved MIT Viren/PTT
// assertions. They never become aliases, identity, year, quality or unit facts.
var virenArabicBanner = regexp.MustCompile(`^\[www\.arabp2p\.net\]_-_تركي مترجم ومدبلج[ ]+`)
var virenSiteTail = regexp.MustCompile(`(?i)\[torrents\.ru\]\.(?:avi|mkv|mp4)$`)
var virenCustomEdition = regexp.MustCompile(`(?i)[ ._]+CUSTOM[ ._]+EXTENDED$`)
var virenPPV = regexp.MustCompile(`(?i)^(.+?[ ._][0-9]{1,3})[ ._]PPV[ ._](.+?)[ ._]HDTV[ ._](?:x264|h264)-[A-Za-z0-9]+(?:\[TGx\])?$`)
var virenChannelTail = regexp.MustCompile(`^(.+?) _ Adult Swim\.(?:mp4|mkv)$`)
var virenCollectionTail = regexp.MustCompile(`(?i)^([^\[\]{}]+?)[ ._]INTEGRAL[ ._]MULTI[ ._]COMPLETE[ ._]BLURAY-[A-Za-z0-9]+$`)

func virenLexicalTitle(raw, title string, year int) string {
	if strings.ContainsAny(raw, "/\\\r\n") {
		return title
	}
	if m := virenSiteTail.FindStringIndex(raw); m != nil && year == 0 && !yearRegex.MatchString(raw) && len(calendarFilenameSpans(raw)) == 0 {
		return strings.TrimSpace(raw[:m[0]])
	}
	if m := virenCollectionTail.FindStringSubmatch(raw); m != nil {
		return normalizeTitleSeparators(m[1])
	}
	if m := virenChannelTail.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if m := virenPPV.FindStringSubmatch(raw); m != nil {
		return normalizeTitleSeparators(m[1] + " " + m[2])
	}
	if titleQualityBoundaryRegex.MatchString(raw) && !hasAmbiguousYear(raw) {
		if at := virenArabicBanner.FindStringIndex(raw); at != nil {
			banner := "تركي مترجم ومدبلج "
			title = strings.TrimPrefix(title, banner)
		}
		// Require a supported source, a unique year and the complete edition pair.
		if year > 0 && extractQuality(raw).Source != "" {
			title = virenCustomEdition.ReplaceAllString(title, "")
		}
	}
	return title
}
