package media

import (
	"regexp"
	"strconv"
	"strings"
)

// These views never enter identity, coverage, year, codec or source inference.
var chillApostrophe = regexp.MustCompile(`([\p{L}])[ ._]+039[ ._]+s[ ._]+`)
var chillWebsite = regexp.MustCompile(`^\[?(?:hchcsen|ToonsHub)\][ ._]+`)
var chillCompleteSeries = regexp.MustCompile(`(?i)[ ._]+Complete[ ._]+Series[ ._]+Seasons[ ._]+[0-9]{1,2}[ ._]+to[ ._]+[0-9]{1,2}(?:$|[ ._])`)
var chillSeasonOnly = regexp.MustCompile(`(?i)^(.+?)[ ._]+S([0-9]{1,2})[ ._]+(?:480p|720p|1080p|2160p)(?:$|[ ._])`)
var chillRepeatedEpisode = regexp.MustCompile(`(?i)^[ ._-]+Episode[ ._]+([0-9]{1,4})(?:$|[ ._])`)
var chillRepeatedTechnicalTail = regexp.MustCompile(`(?i)^[ ._]*(?:(?:480|720|1080|2160)p|WEB[ ._-]?(?:DL|Rip)|HDTV|BluRay|BDRip|x26[45]|H[ .]?26[45]|HEVC|AV1|(?:19|20)[0-9]{2}[-.][0-9]{2}[-.][0-9]{2})(?:$|[ ._-])`)
var chillBracketPublisher = regexp.MustCompile(`\[([a-z][a-z0-9]*_[a-z0-9_]+)\]$`)
var chillTightPublisher = regexp.MustCompile(`(?i)(?:x264|h264|h[ .]264|x265|h265|hevc|av1)-([a-z][a-z0-9]*)[ ]+\([\p{L} ,'-]+(?:Dual-Audio|Multi-Subs)[\p{L} ,'-]*\)$`)

func chillDisplayTitle(raw, title string) string {
	if strings.ContainsAny(raw, "/\\\r\n") {
		return title
	}
	title = chillWebsite.ReplaceAllString(title, "")
	repaired := chillApostrophe.ReplaceAllString(title, "${1}'s ")
	if repaired != title {
		return strings.Join(strings.Fields(repaired), " ")
	}
	return title
}

// Only the immediately repeated scalar is redundant. Leave every other byte
// available to the existing mixed-numbering, date and partial-content guards.
func chillWithoutRepeatedEpisode(name, token string, episodes []int) string {
	remaining := strings.Replace(name, token, "", 1)
	if len(episodes) != 1 {
		return remaining
	}
	index := strings.Index(name, token)
	if index < 0 {
		return remaining
	}
	suffix := name[index+len(token):]
	m := chillRepeatedEpisode.FindStringSubmatchIndex(suffix)
	if m == nil {
		return remaining
	}
	ep, err := strconv.Atoi(suffix[m[2]:m[3]])
	if err != nil || ep != episodes[0] {
		return remaining
	}
	if tail := suffix[m[1]:]; strings.TrimSpace(tail) != "" && !chillRepeatedTechnicalTail.MatchString(tail) {
		return remaining
	}
	return name[:index] + suffix[:m[0]] + suffix[m[1]:]
}

func chillRepairRelease(raw string, res EpisodeRecognition) EpisodeRecognition {
	res.Title = chillDisplayTitle(raw, res.Title)
	if i := strings.Index(raw, " | "); i >= 0 && titleQualityBoundaryRegex.MatchString(raw[:i]) {
		res.Quality.Group = extractQuality(raw[:i]).Group
	}
	if strings.ContainsAny(raw, "/\\\r\n") || hasAmbiguousYear(raw) {
		return res
	}
	if m := chillCompleteSeries.FindStringIndex(raw); m != nil && res.RequiresReview && res.Confidence == 0 && len(res.UnitKeys) == 0 {
		res.MediaType = TypeTV
		if g := chillBracketPublisher.FindStringSubmatch(raw); g != nil {
			res.Quality.Group = g[1]
		}
	}
	if res.Reason == "episode_coverage_unresolved" && res.Confidence == 0 && len(res.UnitKeys) == 0 {
		if m := chillSeasonOnly.FindStringSubmatch(raw); m != nil && !ambiguousEpisodeRegex.MatchString(m[1]) && !competingCoverageRegex.MatchString(m[1]) && len(calendarFilenameSpans(m[1])) == 0 && !strings.ContainsAny(raw, "{}[]") {
			// Display a season-only scope; it never establishes an episode or unit.
			season, _ := strconv.Atoi(m[2])
			title := recognizeFilenameEvidence(m[1])
			if title.Identity == nil && title.Title != "" && title.Year > 0 {
				res.Title, res.Year, res.Season = title.Title, title.Year, season
			}
		}
	}
	if m := chillTightPublisher.FindStringSubmatch(raw); m != nil && res.Quality.Codec != "" {
		res.Quality.Group = m[1]
	}
	return res
}

func chillRepairFilename(raw string, res Recognition) Recognition {
	if spans := standardEpRegex.FindAllStringIndex(raw, -1); len(spans) > 0 && movieTitleEpisodeWordRegex.MatchString(raw[spans[0][1]:]) {
		if release := recognizeReleaseEvidence(raw); release.RequiresReview && release.Confidence == 0 {
			res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
		}
	}
	res.Title = chillDisplayTitle(raw, res.Title)
	if i := strings.Index(raw, " | "); i >= 0 && titleQualityBoundaryRegex.MatchString(raw[:i]) {
		res.Quality.Group = extractQuality(raw[:i]).Group
	}
	if !strings.ContainsAny(raw, "/\\\r\n") {
		if m := chillTightPublisher.FindStringSubmatch(raw); m != nil && res.Quality.Codec != "" {
			res.Quality.Group = m[1]
		}
	}
	return res
}
