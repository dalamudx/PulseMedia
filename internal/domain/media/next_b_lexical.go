package media

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// Display and group views never feed identity or coverage recognition. The
// separator view retains explicit canonical SxxExx and all original raw guards.
// Unbound scope evidence can only remove authority.
var nextBScope = regexp.MustCompile(`(?i)(?:^|[ ._]+)(?:Complete[ ._]+)?(?:S[0-9]{1,2}(?:[ ._]+E[0-9]{1,4}(?:-E?[0-9]{1,4})?)?|E[0-9]{1,4}(?:-E?[0-9]{1,4})?)(?:$|[ ._-])`)
var nextBSegment = regexp.MustCompile(`(?i)(?:^|[ ._])S[0-9]{1,2}E[0-9]{1,4}[A-Z](?:$|[ ._])`)
var nextBCodecGroup = regexp.MustCompile(`(?i)(?:^|[ ._])(?:x26[45]|h[ .]?26[45]|hevc|av1)[ .]*-([A-Za-z][A-Za-z0-9]*(?:-[A-Za-z0-9]+)*)$`)
var nextBCodecSiteGroup = regexp.MustCompile(`(?i)(?:^|[ ._])(?:x26[45]|h[ .]?26[45]|hevc|av1)-([A-Za-z][A-Za-z0-9]*(?:-[A-Za-z0-9]+)+\.[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*)$`)
var nextBCanonicalUnderscore = regexp.MustCompile(`(?i)^[\p{L}\p{N}][\p{L}\p{N} ._'’()-]*_S[0-9]{1,2}E[0-9]{1,4}_-_[\p{L}\p{N}].+$`)
var nextBBracketGroup = regexp.MustCompile(`(?i)(?:^|[ ._])(?:x26[45]|h[ .]?26[45]|hevc|av1)-\[([A-Za-z][A-Za-z0-9-]*)\]$`)
var nextBDottedGroup = regexp.MustCompile(`(?i)[ ._]MULTi-([A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*){1,4})$`)
var nextBSymbolGroup = regexp.MustCompile(`(?i)(?:^|[ ._])(?:x26[45]|h[ .]?26[45]|hevc|av1)[ .]+([\p{S}@][\p{L}\p{N}\p{S}@-]*)$`)
var nextBAudioSymbolGroup = regexp.MustCompile(`(?i)(?:^|[ ._])AC3(?:[ ._][0-9][ ._][0-9][ ._])?([\p{Sc}@][\p{L}\p{N}@-]*)$`)
var nextBArchive = regexp.MustCompile(`(?i)\.(?:rar|7z)$`)
var nextBUnboundDisplay = regexp.MustCompile(`(?i)[ ._]E[0-9]{1,4}(?:-E[0-9]{1,4})?(?:$|[ ._])`)
var nextBCompleteWeb = regexp.MustCompile(`(?i)[ ._]S[0-9]{1,2}[ ._]COMPLETE[ ._](?:480|720|1080|2160)p[ ._](?:NF|AMZN)[ ._]WEB(?:-DL|Rip)(?:$|[ ._])`)
var nextBSourceFirst = regexp.MustCompile(`(?i)[ ]S[0-9]{1,2}[ ]WEB-DL[ ](?:480|720|1080|2160)p[ ]H\.264[ ]DDP[ ]`)
var nextBCompleteRange = regexp.MustCompile(`(?i)[ ]Complete[ ]S[0-9]{1,2}-S[0-9]{1,2}[ ](?:19|20)[0-9]{2}-(?:19|20)[0-9]{2}[ ](?:480|720|1080|2160)p[ ]Blu-ray[ ]`)
var nextBDVDGroup = regexp.MustCompile(`(?i)[ ._]DVD[59]-([A-Za-z][A-Za-z0-9]*)$`)
var nextBTechnicalGroup = regexp.MustCompile(`(?i)DTS-HDMA_[xh]26[45](?:\.mkv)?$`)
var nextBCanonicalPrefix = regexp.MustCompile(`(?i)(?:^|[ ._])S[0-9]{1,2}E[0-9]{1,4}(?:$|[ ._-])`)

func nextBSafe(raw string) bool { return !strings.ContainsAny(raw, "/\\\r\n") }
func nextBTrimContainer(raw string) string {
	for _, ext := range []string{".mkv", ".mp4", ".avi", ".rar", ".zip", ".7z"} {
		if strings.HasSuffix(strings.ToLower(raw), ext) {
			return raw[:len(raw)-len(ext)]
		}
	}
	return raw
}
func nextBGroup(raw, old string, q quality.Profile) string {
	if !nextBSafe(raw) || standardEpisodePublisherPrefix.MatchString(raw) || q.Codec == "" && q.Resolution == "" {
		return old
	}
	name := nextBTrimContainer(raw)
	if nextBTechnicalGroup.MatchString(raw) {
		return ""
	}
	// Dotted site-like suffixes remain raw group evidence, never a guessed relay.
	for _, re := range []*regexp.Regexp{nextBCodecSiteGroup, nextBCodecGroup, nextBBracketGroup, nextBSymbolGroup, nextBAudioSymbolGroup} {

		// The independently certified symbolic publisher layouts are episode releases.
		// A movie's unbound symbol tag remains raw evidence, not a group fact.
		if (re == nextBSymbolGroup || re == nextBAudioSymbolGroup) && !nextBScope.MatchString(raw) && !nextBCanonicalPrefix.MatchString(raw) {
			continue
		}
		if m := re.FindStringSubmatch(name); m != nil {
			candidate := m[1]
			if len(candidate) < 2 || nonGroupMetadataTailRegex.MatchString(candidate) {
				continue
			}
			if re == nextBCodecGroup && !strings.Contains(candidate, "-") && old != "" {
				continue
			}
			return candidate
		}
	}
	if q.Source != "" && q.Resolution != "" {
		if m := nextBDottedGroup.FindStringSubmatch(name); m != nil {
			return m[1]
		}
	}
	return old
}
func nextBCanonicalSeparatorView(name string) string {
	if nextBCanonicalUnderscore.MatchString(name) && !hasAmbiguousYear(name) {
		return strings.ReplaceAll(name, "_", ".")
	}
	return name
}
func nextBRawParentAmbiguity(raw string) bool {
	at := strings.LastIndexAny(raw, "/\\")
	if at <= 0 {
		return false
	}
	parent := strings.ReplaceAll(raw[:at], "_", ".")
	return nextBScope.MatchString(parent) || nextBSegment.MatchString(parent) || standardEpRegex.MatchString(parent) || altEpRegex.MatchString(parent) || unboundCRCEpisodeHintRegex.MatchString(parent)
}
func nextBRefusalDisplay(raw string, q quality.Profile) bool {
	return nextBArchive.MatchString(raw) || nextBCompleteWeb.MatchString(raw) || nextBSourceFirst.MatchString(raw) || nextBCompleteRange.MatchString(raw) || (nextBUnboundDisplay.MatchString(raw) && q.Resolution != "" && q.Codec != "")
}
func nextBDisplayPrefix(raw string, at int) (string, int) {
	prefix := strings.Trim(raw[:at], " ._-")
	if len(prefix) > 2 && prefix[0] == '[' && prefix[len(prefix)-1] == ']' {
		inner := prefix[1 : len(prefix)-1]
		if len(inner) == 4 && strings.IndexFunc(inner, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
			return inner, 0
		}
	}
	title := normalizeTitleSeparators(prefix)
	year, ambiguous := recognizeYear(prefix)
	if year > 0 && !ambiguous {
		// Only remove an explicit trailing year. Do not run technical-token or
		// publisher cleanup over the already delimited literal title.
		stamp := strconv.Itoa(year)
		tail := strings.TrimRight(title, " )]")
		if len(tail) > len(stamp) && strings.HasSuffix(tail, stamp) {
			return strings.TrimRight(tail[:len(tail)-len(stamp)], " (["), year
		}
	}
	return title, 0
}
func nextBDVDDisplay(raw, title, group string) string {
	if hasAmbiguousYear(raw) {
		if m := nextBDVDGroup.FindStringSubmatch(raw); m != nil && m[1] == group {
			return strings.TrimSuffix(title, "-"+group)
		}
	}
	return title
}
func nextBRepairRelease(raw string, res EpisodeRecognition) EpisodeRecognition {
	if res.Reason == "conflicting_episode_coverage" && unboundCRCEpisodeHintRegex.MatchString(raw) {
		res.Season = 0
	}
	if nextBRawParentAmbiguity(raw) {
		res.MediaType, res.Season, res.AbsoluteEpisode = TypeTV, 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime = false
		res.Confidence, res.RequiresReview = 0, true
		return res
	}
	if !nextBSafe(raw) {
		return res
	}
	res.Quality.Group = nextBGroup(raw, res.Quality.Group, res.Quality)
	res.Title = nextBDVDDisplay(raw, res.Title, res.Quality.Group)
	// Canonical tokens remain the existing parser's responsibility, including S00.
	if at := nextBSegment.FindStringIndex(raw); at != nil {
		res.Title, res.Year = nextBDisplayPrefix(raw, at[0])
		res.MediaType = TypeTV
		res.Season, res.AbsoluteEpisode = 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime = false
		res.Confidence, res.RequiresReview = 0, true
		if res.Reason != "non_video_file" {
			res.Reason = "partial_episode_content"
		}
		return res
	}
	if res.RequiresReview && res.Confidence == 0 && len(res.UnitKeys) == 0 && res.AbsoluteEpisode == 0 && nextBRefusalDisplay(raw, res.Quality) {
		if at := nextBScope.FindStringIndex(raw); at != nil && res.MediaType == TypeTV {
			res.Season = 0
			res.Title, res.Year = nextBDisplayPrefix(raw, at[0])
			if nextBUnboundDisplay.MatchString(raw) {
				res.Year, _ = recognizeYear(raw)
			}
		}
	}
	// Restore a paired parenthesis belonging to a canonical title, not metadata.
	if !res.RequiresReview && res.Reason == "standard_season_episode" {
		if at := nextBCanonicalPrefix.FindStringIndex(raw); at != nil {
			prefix := strings.Trim(raw[:at[0]], " ._-")
			if strings.Contains(prefix, "(") && strings.HasSuffix(prefix, ")") && !hasAmbiguousYear(prefix) {
				title := stripSeriesReleaseYear(normalizeTitleSeparators(prefix), res.Year)
				if strings.TrimRight(title, ")") == res.Title && strings.Count(title, "(") == strings.Count(title, ")") {
					res.Title = title
				}
			}
		}
	}
	return res
}
func nextBRepairMovie(raw string, res Recognition) Recognition {
	if nextBRawParentAmbiguity(raw) {
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
		return res
	}
	if !nextBSafe(raw) {
		return res
	}
	res.Quality.Group = nextBGroup(raw, res.Quality.Group, res.Quality)
	res.Title = nextBDVDDisplay(raw, res.Title, res.Quality.Group)
	if at := nextBSegment.FindStringIndex(raw); at != nil {
		res.Title, res.Year = nextBDisplayPrefix(raw, at[0])
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
	} else if at := nextBScope.FindStringIndex(raw); at != nil && res.RequiresReview && res.Confidence == 0 && nextBRefusalDisplay(raw, res.Quality) {
		res.Title, res.Year = nextBDisplayPrefix(raw, at[0])
	}
	return res
}
