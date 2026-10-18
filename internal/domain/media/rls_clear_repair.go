package media

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Display cleanup never feeds identity, coverage, year or quality recognition.
var rlsSubpack = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])subpack(?:$|[^a-z0-9])`)
var rlsExtras = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:digital[ ._-]+)?extras(?:$|[^a-z0-9])`)
var rlsExtrasOnly = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])extras[ ._-]+only(?:$|[^a-z0-9])`)
var rlsPresentationPrefix = regexp.MustCompile(`^(?:\(Comedy\) Netflix Originals - |IMAX\.-\.)`)
var rlsGermanCut = regexp.MustCompile(`(?i)[ ._-]+UNCUT[ ._-]+German(?:[ ._-]+(?:19|20)[0-9]{2})?[ ._-]+DL(?:$|[ ._-])`)
var rlsEditionTail = regexp.MustCompile(`(?i)[ ._-]+(?:[0-9]{1,2}(?:st|nd|rd|th)[ ._-]+Anniversary[ ._-]+Edition[ ._-]+)?Director'?s[ ._-]+Cut$`)
var rlsTorrentGroup = regexp.MustCompile(`(?i)-([a-z][a-z0-9]{1,31})\.torrent$`)
var rlsPreYearBluray = regexp.MustCompile(`(?i)[ ._-]+BluRay[ ._-]+(?:19|20)[0-9]{2}[ ._-]+German[ ._-]+DTS$`)

func rlsPartialEvidence(raw string) bool {
	if rlsSubpack.MatchString(raw) && titleQualityBoundaryRegex.MatchString(raw) {
		year := yearRegex.FindStringIndex(raw)
		if year == nil {
			return true
		}
		for _, marker := range rlsSubpack.FindAllStringIndex(raw, -1) {
			if marker[0] >= year[1] {
				return true
			}
		}
	}
	if !titleQualityBoundaryRegex.MatchString(raw) {
		return false
	}
	if rlsExtrasOnly.MatchString(raw) && yearRegex.MatchString(raw) {
		return true
	}
	if year := yearRegex.FindStringIndex(raw); year != nil {
		return rlsExtras.MatchString(raw[year[1]:])
	}
	return false
}

func rlsLexicalRepair(raw, title string, year int) string {
	if strings.ContainsAny(raw, "/\\\r\n") || hasAmbiguousYear(raw) {
		return title
	}
	if year > 0 && !hasAmbiguousYear(raw) && titleQualityBoundaryRegex.MatchString(raw) {
		if span := rlsPresentationPrefix.FindStringIndex(raw); span != nil {
			// Existing trimMovieTitleEdges removes the opening parenthesis.
			prefix := strings.TrimLeft(normalizeTitleSeparators(raw[:span[1]]), "(")
			if strings.HasPrefix(title, prefix) {
				title = strings.TrimLeft(strings.TrimPrefix(title, prefix), " ._-")
			}
		}
		if extractQuality(raw).Source != "" {
			title = rlsEditionTail.ReplaceAllString(title, "")
		}
		if rlsExtrasOnly.MatchString(raw) {
			if span := rlsExtrasOnly.FindStringIndex(title); span != nil {
				title = strings.TrimRight(title[:span[0]], " ._-")
			}
		}
	}
	if titleQualityBoundaryRegex.MatchString(raw) {
		if span := rlsGermanCut.FindStringIndex(raw); span != nil {
			prefix := normalizeTitleSeparators(raw[:span[0]])
			if strings.HasPrefix(title, prefix) {
				title = prefix
			}
		}
	}
	if span := rlsPreYearBluray.FindStringIndex(raw); span != nil {
		title = normalizeTitleSeparators(raw[:span[0]])
	}
	return title
}

// Explicit technical TV disc/multiple-season layouts are refusal evidence only.
var rlsDiscScopes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[ ._-]+S[0-9]{1,2}(?:S[0-9]{1,2})+[ ._-]+COMPLETE(?:$|[ ._/-])`),
	regexp.MustCompile(`(?i)[ ._-]+S[0-9]{1,2}D[0-9]{1,2}[ ._-]+COMPLETE[ ._-]+BLURAY(?:$|[ ._/-])`),
	regexp.MustCompile(`(?i)[ ._-]+Series[ ._-]+[0-9]{1,2}[ ._-]+Disk[ ._-]+[1-9][0-9]?[ ._-]+of[ ._-]+[1-9][0-9]?[ ._-]+ISO[ ._-]+DVDR(?:$|[ ._/-])`),
}

func rlsDiscScope(raw string) int {
	for _, form := range rlsDiscScopes {
		if span := form.FindStringIndex(raw); span != nil {
			return span[0]
		}
	}
	return -1
}

func rlsRepairRelease(raw string, res EpisodeRecognition) EpisodeRecognition {
	if at := rlsDiscScope(raw); at >= 0 {
		res.MediaType, res.Season, res.AbsoluteEpisode = TypeTV, 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview, res.Confidence = false, true, 0
		if res.Reason != "non_video_file" {
			res.Reason = "multi_unit_collection"
		}
		if !strings.ContainsAny(raw, "/\\\r\n") && !hasAmbiguousYear(raw) {
			lexical := recognizeFilenameEvidence(raw[:at])
			res.Title, res.Year = lexical.Title, lexical.Year
		}
	}
	if rlsPartialEvidence(raw) {
		res.Season, res.AbsoluteEpisode = 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview, res.Confidence = false, true, 0
		if res.Reason != "non_video_file" {
			res.Reason = "partial_movie_content"
		}
	}
	// Complete raw collection evidence survives a clean basename suffix.
	if collectionCoverage(raw) && !collectionCoverage(filepath.Base(raw)) {
		res.Season, res.AbsoluteEpisode = 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview, res.Confidence = false, true, 0
		if res.Reason != "non_video_file" {
			res.Reason = "multi_unit_collection"
		}
	}
	res.Title = rlsLexicalRepair(raw, res.Title, res.Year)
	if res.Quality.Group == "" && res.Quality.Resolution != "" && res.Quality.Source != "" && res.Quality.Codec != "" {
		if match := rlsTorrentGroup.FindStringSubmatch(raw); match != nil {
			res.Quality.Group = match[1]
		}
	}
	return res
}

func rlsRepairMovie(raw string, res Recognition) Recognition {
	if rlsDiscScope(raw) >= 0 || rlsPartialEvidence(raw) || collectionCoverage(raw) && !collectionCoverage(filepath.Base(raw)) {
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
	}
	res.Title = rlsLexicalRepair(raw, res.Title, res.Year)
	if res.Quality.Group == "" && res.Quality.Resolution != "" && res.Quality.Source != "" && res.Quality.Codec != "" {
		if match := rlsTorrentGroup.FindStringSubmatch(raw); match != nil {
			res.Quality.Group = match[1]
		}
	}
	return res
}
