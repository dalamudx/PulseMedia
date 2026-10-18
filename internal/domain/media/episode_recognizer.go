package media

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const EpisodeParserVersion = "episode.v74"

var multiEpRegex1 = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,4})[-~]E?(\d{1,4})\b`)
var dashedEpisodeListRegex = regexp.MustCompile(`(?i)\bS(\d{1,2})(E\d{1,4}(?:-E\d{1,4}){2,})\b`)
var chainEpRegex = regexp.MustCompile(`(?i)\bS(\d{1,2})((?:E\d{1,4}){2,})\b`)
var episodeNumbersRegex = regexp.MustCompile(`(?i)E(\d{1,4})`)
var standardEpRegex = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,4})\b`)
var altMultiEpRegex = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,4})[-~](\d{1,4})\b`)
var altEpRegex = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,4})\b`)
var animeRegex1 = regexp.MustCompile(`(?i)\[([^\]]+)\]\s*([^\[]+?)\s*-\s*(?:EP|E)?(\d{1,4})(?:v\d)?\s*(?:\((?:480|720|1080|2160)p\)\s*)?\[`)
var absoluteEpisodeHintRegex = regexp.MustCompile(`(?i)^\[[^\]]+\]\s*[^\[]+?\s*-\s*(?:EP|E)?[0-9]{1,4}(?:v[0-9]+)?\b`)
var unboundCRCEpisodeHintRegex = regexp.MustCompile(`(?i)^\[[^\]\r\n]{1,64}\][ ._]+[^\[\]\r\n]+?[ ._]+[0-9]{1,3}(?:v[0-9]+)?[ ._]+(?:\[(?:480|720|1080|2160)p\][ ._]*)?\[[0-9a-f]{8}\](?:$|[ ._/-])`)
var localizedEpisodeRangeHintRegex = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])cap[ ._-]+[0-9]{1,4}[ ._-]+al[ ._-]+[0-9]{1,4}\b`)
var extraAbsoluteCoverageRegex = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])[-+~]\s*(?:EP|E)?[0-9]{1,4}(?:v[0-9]+)?(?:$|\b)`)

var ambiguousEpisodeRegex = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:S\d+(?:E\d|\b)|E\d{1,4}\b|\d{1,2}x\d)`)

var movieTitleEpisodeWordRegex = regexp.MustCompile(`(?i)\bepisode[ ._-]+[0-9]{1,3}\b`)
var movieTitleSubtitleRegex = regexp.MustCompile(`[\p{L}]`)

var competingCoverageRegex = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:S[0-9]+(?:\b|E[0-9])|E[0-9]{1,4}\b|seasons?[ ._-]+(?:[0-9]+\b|one\b|two\b|three\b)|episodes?[ ._-]+(?:[0-9]+\b|one\b|two\b|three\b)|cap[ ._-]+[0-9]{1,4}[ ._-]+al[ ._-]+[0-9]{1,4}\b|EPs?[ ._-]*[0-9]{1,4}\s*[-~]\s*(?:EPs?[ ._-]*)?[0-9]{1,4}\b)`)
var localizedTVHintRegex = regexp.MustCompile(`(?i)(?:国漫|动漫|(?:^|[^\p{L}])(?:сезон|серии|temporada|sezon|bölüm)(?:[^\p{L}]|$))`)
var frenchSeasonNumberHintRegex = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])saisons?[ ._-]+[0-9]{1,2}\b`)
var brokenEpisodeSuffixRegex = regexp.MustCompile(`^(?:[._][0-9]+(?:\b|$)|[-~](?:[eE0-9]|$|[. _])|x[0-9])`)
var partialEpisodeSuffixRegex = regexp.MustCompile(`(?i)\b(?:sample|clip|trailer|first[ ._-]*half|second[ ._-]*half)\b`)

type EpisodeRecognition struct {
	RawName         string
	MediaType       Type
	Title           string
	Year            int
	Season          int
	Episodes        []int
	UnitKeys        []string
	IsAnime         bool
	AbsoluteEpisode int
	Quality         quality.Profile
	Confidence      float64
	Reason          string
	RequiresReview  bool
	ParserVersion   string
}

func recognizeReleaseEvidence(filename string) EpisodeRecognition {
	if view, subtitle := subtitleFileTitleView(filename); subtitle {
		res := RecognizeRelease(view)
		res.RawName = filename
		res.Season, res.AbsoluteEpisode = 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime = false
		res.Confidence = 0
		res.RequiresReview = true
		res.Reason = "non_video_file"
		return res
	}
	rawInput := filename
	rawBase := filepath.Base(filename)
	base := releaseTitleSitePrefixRegex.ReplaceAllString(rawBase, "")
	filename = releaseTitleSitePrefixRegex.ReplaceAllString(filename, "")
	ext := filepath.Ext(base)
	switch strings.ToLower(ext) {
	case ".mkv", ".mp4", ".avi", ".m4v", ".mov", ".wmv", ".ts", ".m2ts", ".iso":
	default:
		ext = ""
	}
	name := strings.TrimSuffix(base, ext)
	res := EpisodeRecognition{RawName: rawInput, MediaType: TypeMovie, Quality: extractQuality(name), ParserVersion: EpisodeParserVersion}
	name = maskExplicitAudioCounts(name)

	// Preserve explicit canonical coordinates while normalizing their separators.
	name = nextBCanonicalSeparatorView(name)
	if collectionCoverage(name) || allSeasonsCoverageRegex.MatchString(filename) {
		legacy := Recognize(explicitCollectionTitle(base))
		res.Title, res.Year = legacy.Title, legacy.Year
		res.RequiresReview = true
		res.Reason = "multi_unit_collection"
		return res
	}
	hold := func(reason string) EpisodeRecognition {
		res.MediaType = TypeTV
		res.RequiresReview = true
		res.Confidence = 0
		res.Reason = reason
		res.UnitKeys = nil
		res.Episodes = nil
		return res
	}
	if hasUnboundBroadcastCoverage(filename) {
		if standardEpRegex.MatchString(filename) || altEpRegex.MatchString(filename) || absoluteEpisodeHintRegex.MatchString(filename) {
			return hold("conflicting_episode_coverage")
		}
		return hold("episode_coverage_unresolved")
	}
	if hasParentEpisodeCoverage(filename) {
		return hold("episode_coverage_unresolved")
	}
	if unboundOVATechnicalHint.MatchString(filename) {
		return hold("episode_coverage_unresolved")
	}
	if hint := publisherEpisodeRangeHint(filename); hint != nil {
		if filepath.Base(filename) != filename || standardEpRegex.MatchString(filename) || altEpRegex.MatchString(filename) || len(calendarFilenameSpans(hint.title)) != 0 {
			return hold("conflicting_episode_coverage")
		}
		res.Title = cleanTitle(hint.title)
		res.Quality.Group = hint.publisher
		return hold("episode_coverage_unresolved")
	}
	if hint := unboundTechnicalEpisodeHint(filename); hint != nil {
		if filepath.Base(filename) != filename || standardEpRegex.MatchString(filename) || altEpRegex.MatchString(filename) || absoluteEpisodeHintRegex.MatchString(filename) {
			return hold("conflicting_episode_coverage")
		}
		res.Title = cleanTitle(hint.title)
		if hint.publisher != "" {
			res.Quality.Group = hint.publisher
		}
		return hold("episode_coverage_unresolved")
	}
	if parent := filepath.Dir(filename); parent != "." && (localizedEpisodeRangeHintRegex.MatchString(parent) || unboundCRCEpisodeHintRegex.MatchString(parent) || frenchSeasonNumberHintRegex.MatchString(parent) || coffretCollectionCoverageRegex.MatchString(parent)) {
		return hold("conflicting_episode_coverage")
	}
	finish := func(token string, season int, episodes []int, reason string) EpisodeRecognition {
		if len(episodes) == 0 || len(episodes) > 20 {
			return hold("invalid_episode_coverage")
		}
		res.MediaType = TypeTV
		res.Season = season
		index := strings.Index(strings.ToUpper(name), strings.ToUpper(token))
		if index > 0 {
			title, publisher := standardEpisodeTitleView(name[:index])
			res.Title = cleanTitle(title)
			if publisher != "" {
				if res.Quality.Group == "" || strings.EqualFold(res.Quality.Group, publisher) {
					res.Quality.Group = publisher
				} else {
					res.Quality.Group = ""
				}
			}
		}
		var ambiguousYear bool
		res.Year, ambiguousYear = recognizeYear(name)
		if ambiguousYear {
			return hold("ambiguous_media_year")
		}
		res.Title = stripSeriesReleaseYear(res.Title, res.Year)
		// An explicitly numbered episode may carry a valid calendar air-date.
		// Invalid stamps and additional numbers stay visible to the coverage guards.
		for _, date := range calendarFilenameSpans(name) {
			if !validCalendarStamp(name[date[0]:date[1]]) {
				return hold("invalid_calendar_coverage")
			}
		}
		suffix := skipLeadingCalendarStamps(name[index+len(token):])
		suffix = skipRepeatedNumericSeriesTitle(suffix, res.Title)
		if brokenEpisodeSuffixRegex.MatchString(suffix) {
			return hold("episode_coverage_unresolved")
		}
		if partialEpisodeSuffixRegex.MatchString(suffix) {
			return hold("partial_episode_content")
		}
		seen := map[int]bool{}
		for _, ep := range episodes {
			if ep < 1 || seen[ep] {
				return hold("invalid_episode_coverage")
			}
			seen[ep] = true
		}
		remaining := chillWithoutRepeatedEpisode(name, token, episodes)
		if len(standardEpRegex.FindAllString(remaining, -1)) > 0 || len(altEpRegex.FindAllString(remaining, -1)) > 0 || competingCoverageRegex.MatchString(remaining) || unboundCRCEpisodeHintRegex.MatchString(remaining) || frenchSeasonNumberHintRegex.MatchString(remaining) {
			return hold("conflicting_episode_coverage")
		}
		if res.Title == "" {
			return hold("missing_media_title")
		}
		res.Episodes = episodes
		for _, ep := range episodes {
			res.UnitKeys = append(res.UnitKeys, FormatEpisodeUnitKey(season, ep))
		}
		res.Confidence = .95
		res.Reason = reason
		return res
	}
	// An absolute token is independent coverage evidence, even when a standard token
	// occurs elsewhere. A mapping can never turn partial or mixed coverage into a unit.
	if absolute := animeRegex1.FindStringSubmatch(name); len(absolute) > 3 {
		if partialEpisodeSuffixRegex.MatchString(name) {
			return hold("partial_episode_content")
		}
		remaining := strings.Replace(name, absolute[0], absolute[2]+" ", 1)
		if standardEpRegex.MatchString(name) || altEpRegex.MatchString(name) || competingCoverageRegex.MatchString(remaining) || extraAbsoluteCoverageRegex.MatchString(remaining) || frenchSeasonNumberHintRegex.MatchString(remaining) {
			return hold("conflicting_episode_coverage")
		}
		if hasAmbiguousYear(name) {
			return hold("ambiguous_media_year")
		}
	}

	if absoluteEpisodeHintRegex.MatchString(name) && !animeRegex1.MatchString(name) {
		if partialEpisodeSuffixRegex.MatchString(name) {
			return hold("partial_episode_content")
		}
		return hold("episode_coverage_unresolved")
	}
	// Three or more explicitly prefixed E terms form a list. A two-endpoint
	// range keeps its existing meaning; bare-number chains remain unresolved.
	if m := dashedEpisodeListRegex.FindStringSubmatch(name); len(m) > 2 {
		season, _ := strconv.Atoi(m[1])
		eps := []int{}
		for _, number := range episodeNumbersRegex.FindAllStringSubmatch(m[2], -1) {
			ep, _ := strconv.Atoi(number[1])
			eps = append(eps, ep)
		}
		if len(eps) > 20 {
			return hold("invalid_episode_coverage")
		}
		return finish(m[0], season, eps, "explicit_episode_list")
	}
	if m := multiEpRegex1.FindStringSubmatch(name); len(m) > 3 {
		season, _ := strconv.Atoi(m[1])
		start, _ := strconv.Atoi(m[2])
		end, _ := strconv.Atoi(m[3])
		if start < 1 || end < start || end-start >= 20 {
			return hold("invalid_episode_range")
		}
		eps := []int{}
		for ep := start; ep <= end; ep++ {
			eps = append(eps, ep)
		}
		return finish(m[0], season, eps, "multi_episode_range")
	}
	if m := chainEpRegex.FindStringSubmatch(name); len(m) > 2 {
		season, _ := strconv.Atoi(m[1])
		eps := []int{}
		for _, number := range episodeNumbersRegex.FindAllStringSubmatch(m[2], -1) {
			ep, _ := strconv.Atoi(number[1])
			eps = append(eps, ep)
		}
		return finish(m[0], season, eps, "explicit_episode_list")
	}
	if m := standardEpRegex.FindStringSubmatch(name); len(m) > 2 {
		season, _ := strconv.Atoi(m[1])
		ep, _ := strconv.Atoi(m[2])
		return finish(m[0], season, []int{ep}, "standard_season_episode")
	}
	if m := altMultiEpRegex.FindStringSubmatch(name); len(m) > 3 {
		season, _ := strconv.Atoi(m[1])
		start, _ := strconv.Atoi(m[2])
		end, _ := strconv.Atoi(m[3])
		if start < 1 || end < start || end-start >= 20 {
			return hold("invalid_episode_range")
		}
		eps := []int{}
		for ep := start; ep <= end; ep++ {
			eps = append(eps, ep)
		}
		return finish(m[0], season, eps, "alt_multi_episode_range")
	}
	if m := altEpRegex.FindStringSubmatch(name); len(m) > 2 {
		season, _ := strconv.Atoi(m[1])
		ep, _ := strconv.Atoi(m[2])
		return finish(m[0], season, []int{ep}, "alt_season_episode")
	}
	if m := animeRegex1.FindStringSubmatch(name); len(m) > 3 {
		ep, _ := strconv.Atoi(m[3])
		if ep < 1 {
			return hold("invalid_absolute_episode")
		}
		res.IsAnime = true
		res.AbsoluteEpisode = ep
		res.Title = cleanTitle(m[2])
		res.Quality.Group = strings.TrimSpace(m[1])
		return hold("absolute_mapping_required")
	}
	if dates := calendarFilenameSpans(name); len(dates) > 0 {
		res.Title = cleanTitle(name[:dates[0][0]])
		return hold("calendar_coverage_unresolved")
	}
	rawCoverage := maskMovieTitleEpisodeWord(maskExplicitAudioCounts(filename))
	if len(calendarFilenameSpans(rawCoverage)) > 0 {
		return hold("calendar_coverage_unresolved")
	}
	if ambiguousEpisodeRegex.MatchString(rawCoverage) || competingCoverageRegex.MatchString(rawCoverage) || localizedTVHintRegex.MatchString(rawCoverage) || unboundCRCEpisodeHintRegex.MatchString(rawCoverage) || frenchSeasonNumberHintRegex.MatchString(rawCoverage) {
		return hold("episode_coverage_unresolved")
	}
	movie := Recognize(filename)
	res.Title, res.Year, res.MediaType = movie.Title, movie.Year, TypeMovie
	res.UnitKeys = []string{FormatMovieUnitKey()}
	res.Confidence, res.RequiresReview = movie.Confidence, movie.RequiresReview
	if movie.RequiresReview {
		res.UnitKeys = nil
	}
	switch {
	case partialMovieSuffix(name):
		res.Reason = "partial_movie_content"
	case movie.RequiresReview && hasAmbiguousYear(name):
		res.Reason = "ambiguous_media_year"
	case movie.Identity != nil:
		res.Reason = "has_external_id"
	case res.Year > 0:
		res.Reason = "clean_movie_with_year"
	case res.Confidence >= .6:
		res.Reason = "title_only_without_year"
	default:
		res.Reason = "low_confidence_unidentified"
	}
	return res
}
func hasAmbiguousYear(s string) bool { _, ambiguous := recognizeYear(s); return ambiguous }
func cleanTitle(s string) string {
	s = stripReleaseTitleDecorations(s)
	s = normalizeTitleSeparators(s)
	s = tmdbIDRegex.ReplaceAllString(s, "")
	s = imdbIDRegex.ReplaceAllString(s, "")
	s = noiseRegex.ReplaceAllString(s, "")
	return strings.Trim(strings.TrimSpace(s), " -_()[]{}")
}

// A numbered Episode word can be part of a movie title, when a subtitle and
// unambiguous work year follow it. Mask only that word for the loose TV hint;
// explicit season/episode, calendar and absolute-number evidence stays intact.
func maskMovieTitleEpisodeWord(name string) string {
	year, ambiguous := recognizeYear(name)
	if year == 0 || ambiguous || ambiguousEpisodeRegex.MatchString(name) || animeRegex1.MatchString(name) || len(calendarFilenameSpans(name)) > 0 || localizedTVHintRegex.MatchString(name) || frenchSeasonNumberHintRegex.MatchString(name) {
		return name
	}
	matches := movieTitleEpisodeWordRegex.FindAllStringIndex(name, -1)
	if len(matches) != 1 {
		return name
	}
	span := matches[0]
	yearIndex := strings.Index(name, strconv.Itoa(year))
	if span[1] >= yearIndex || strings.Trim(name[:span[0]], " ._-") == "" {
		return name
	}
	if quality := titleQualityBoundaryRegex.FindStringIndex(name); quality != nil && quality[0] < yearIndex {
		return name
	}
	if !movieTitleSubtitleRegex.MatchString(name[span[1]:yearIndex]) {
		return name
	}
	return name[:span[0]] + strings.Repeat(" ", span[1]-span[0]) + name[span[1]:]
}

// A repeated four-digit series title is an episode subtitle only when the
// following token is release quality. Distinct or further numbers stay held.
func skipRepeatedNumericSeriesTitle(suffix, title string) string {
	if len(title) != 4 {
		return suffix
	}
	for _, c := range title {
		if c < '0' || c > '9' {
			return suffix
		}
	}
	for _, separator := range []string{".", "_", " "} {
		prefix := separator + title
		if !strings.HasPrefix(suffix, prefix) {
			continue
		}
		rest := strings.TrimPrefix(suffix, prefix)
		qualityName := strings.TrimLeft(rest, "._ ")
		if qualityName == rest {
			continue
		}
		if marker := titleQualityBoundaryRegex.FindStringIndex(qualityName); marker != nil && marker[0] == 0 {
			return rest
		}
	}
	return suffix
}
