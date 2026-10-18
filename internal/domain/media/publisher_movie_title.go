package media

import (
	"regexp"
	"strings"
)

// These publisher spellings and complete technical tails are independently
// asserted by the fixed Rakun source. Keep arbitrary publishers/brackets raw.
var certifiedMoviePublisherPrefix = regexp.MustCompile(`(?i)^\[(?:Erai-raws|Dragonier)\][ \t]+`)
var certifiedMoviePublisherTail = regexp.MustCompile(`(?i)[ \t]+\[(?:BD[ \t]+)?(?:480p|720p|1080p|2160p)\](?:[ \t]*\[(?:AVC-YUV444P10|TrueHD|6\.1ch[ \t]+-[ \t]+FLAC|[a-f0-9]{8})\])*[ \t]*$`)
var unboundOVATechnicalHint = regexp.MustCompile(`(?i)\bOVA[ ._-]+\[(?:BDRip|BRRip|BD|BluRay|Blu-ray|480p|720p|1080p|2160p)(?:[ ._-]|\])`)

func stripCertifiedPublisherMovieTitle(s string) string {
	prefix := certifiedMoviePublisherPrefix.FindStringIndex(s)
	if prefix == nil {
		return s
	}
	body := s[prefix[1]:]
	tail := certifiedMoviePublisherTail.FindStringIndex(body)
	if tail == nil {
		return s
	}
	return strings.TrimSpace(body[:tail[0]])
}
