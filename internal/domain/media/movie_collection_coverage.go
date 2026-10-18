package media

import (
	"regexp"
	"strings"
)

// Compound collection declarations or an explicit count of movies/seasons do
// not prove one canonical unit. Single-word work titles (e.g. The Collection)
// are not rejected. This never invents the contained works or episode range.
var collectionCoverageRegex = regexp.MustCompile(`(?i)\b(?:(?:complete|ultimate)[ ._-]+(?:collection|pack|set|series)|full[ ._-]+series|all[ ._-]+[0-9]{1,3}[ ._-]+seasons?|[0-9]{1,3}[ ._-]+movies|box[ ._-]*set)\b`)

var allSeasonsCoverageRegex = regexp.MustCompile(`(?i)\b(?:all[ ._-]+seasons?[ ._-]*\(?[ ._-]*[1-9][0-9]?[ ._-]*(?:-|to|thru|through)[ ._-]*[1-9][0-9]?(?:$|[^0-9a-z])|seasons?[ ._-]+[1-9][0-9]?[ ._-]*(?:-|to|thru|through)[ ._-]*[1-9][0-9]?[ ._-]+complete\b)`)
var beforeAllSeasonsCompleteRegex = regexp.MustCompile(`(?i)[ ._-]+complete$`)

// French complete-package markers require a second package/language declaration.
// A standalone film named Integral/Intégrale remains a work title.
var frenchCollectionCoverageRegex = regexp.MustCompile(`(?i)\b(?:int[ée]grale?s?[ ._-]+(?:multi|complete)|(?:multi|complete)[ ._-]+int[ée]grale?s?)\b`)

// COMPLETE discs alone are not multi-work evidence. The paired French
// box-set declaration requires the explicit Blu-ray and Coffret words.
var coffretCollectionCoverageRegex = regexp.MustCompile(`(?i)\bcomplete[ ._-]+(?:bluray|blu-ray)[ ._-]+coffret\b`)
var completeSeriesExtrasRegex = regexp.MustCompile(`(?i)\bcomplete[ ._-]+series[ ._-]*\+[ ._-]*(?:ova|oav)s?\b`)
var seriesExtrasTechnicalTailRegex = regexp.MustCompile(`(?i)^(?:[ ._-]*\((?:high[ ._-]+quality|dual[ ._-]+audio)\))*[ ._-]*(?:mkv[ ._-]+)?(?:\[(?:480p|720p|1080p|2160p)\]|(?:480p|720p|1080p|2160p|bluray|blu-ray|bdrip)\b)`)

// A numbered multi-work suffix must follow work text and terminate at a date,
// a technical/identity marker or the filename end. Embedded work-title words
// (Trilogy of Terror), bare Trilogy and article-only prefixes remain titles.
var numberedCollectionSuffixRegex = regexp.MustCompile(`(?i)\b(?:duology|trilogy|tetralogy|quadrilogy|quadrology|pentalogy|hexalogy|heptalogy|octalogy|octology|ennealogy|nonalogy|decalogy)\b(?:[ ._-]+(?:swesub|multi|truefrench|french|german|english|subbed|dubbed)){0,3}(?:[ ._-]*(?:$|\.(?:mkv|mp4|avi)$)|[ ._-]+(?:19[0-9]{2}|20[0-9]{2}|480p|720p|1080p|2160p|4k|bluray|blu-ray|bdrip|brrip|web-dl|webrip|hdtv|remux|dvd|dvdr|dvdrip|cam|telesync|telecine|xvid|divx|x264|h264|x265|hevc)\b|[ ._-]*[({\[]\s*(?:19[0-9]{2}|20[0-9]{2}|tmdb[ ._-]|imdb[ ._-]))`)

// Explicit release metadata, not ordinary movie-title words.
var bracketCompleteCoverageRegex = regexp.MustCompile(`(?i)\[complete\]`)
var completeReleaseTailRegex = regexp.MustCompile(`(?i)^\s*(?:\[[^\[\]]+\]\s*)*(?:$|\.(?:mkv|mp4|avi)$|[ ._-]+(?:19[0-9]{2}|20[0-9]{2}|480p|720p|1080p|2160p|4k|bluray|blu-ray|bdrip|brrip|web-dl|webrip|hdtv|bdremux|remux|x264|h264|x265|hevc)\b|[ ._-]*\{(?:tmdb-|imdb-))`)
var bracketCollectionSuffixRegex = regexp.MustCompile(`(?i)\bcollection[ ._-]*\[(?:[a-z0-9]+[ ._-]+)*(?:480p|720p|1080p|2160p)\b`)
var countedMovieCollectionRegex = regexp.MustCompile(`(?i)\b[0-9]{1,3}[ ._-]+movie[ ._-]+collection\b`)
var completeVersionTailRegex = regexp.MustCompile(`(?i)(?:\s*\[(?:v[0-9]{1,2}|R2J|VFR|dual[ ._-]+audio)\])+\s*$`)

// Only new explicit declarations provide a narrower lexical title view. The
// full input and unrecognized publisher brackets remain unchanged evidence.
func explicitCollectionTitle(name string) string {
	if span := allSeasonsCoverageRegex.FindStringIndex(name); span != nil && explicitCollectionPrefix(name[:span[0]]) {
		prefix := strings.Trim(name[:span[0]], " ,._-(")
		return beforeAllSeasonsCompleteRegex.ReplaceAllString(prefix, "")
	}
	if span := completeSeriesExtrasRegex.FindStringIndex(name); span != nil && explicitCollectionPrefix(name[:span[0]]) && seriesExtrasTechnicalTailRegex.MatchString(name[span[1]:]) {
		return name[:span[0]]
	}
	if span := bracketCompleteCoverageRegex.FindStringIndex(name); span != nil && explicitCollectionPrefix(name[:span[0]]) && completeReleaseTailRegex.MatchString(name[span[1]:]) {
		return completeVersionTailRegex.ReplaceAllString(name[:span[0]], "")
	}
	if span := bracketCollectionSuffixRegex.FindStringIndex(name); span != nil && explicitCollectionPrefix(name[:span[0]]) {
		return name[:span[0]]
	}
	return name
}

func explicitCollectionPrefix(s string) bool {
	s = strings.Trim(s, " ._-[]()")
	return s != "" && !strings.EqualFold(s, "the") && !strings.EqualFold(s, "a") && !strings.EqualFold(s, "an")
}

func collectionCoverage(name string) bool {
	if allSeasonsCoverageRegex.MatchString(name) || collectionCoverageRegex.MatchString(name) || frenchCollectionCoverageRegex.MatchString(name) || coffretCollectionCoverageRegex.MatchString(name) {
		return true
	}
	if countedMovieCollectionRegex.MatchString(name) {
		return true
	}
	if span := bracketCompleteCoverageRegex.FindStringIndex(name); span != nil && explicitCollectionPrefix(name[:span[0]]) && completeReleaseTailRegex.MatchString(name[span[1]:]) {
		return true
	}
	if span := bracketCollectionSuffixRegex.FindStringIndex(name); span != nil && explicitCollectionPrefix(name[:span[0]]) {
		return true
	}
	for _, span := range numberedCollectionSuffixRegex.FindAllStringIndex(name, -1) {
		prefix := strings.Trim(name[:span[0]], " ._-[]()")
		if prefix != "" && !strings.EqualFold(prefix, "the") && !strings.EqualFold(prefix, "a") && !strings.EqualFold(prefix, "an") {
			return true
		}
	}
	return false
}
