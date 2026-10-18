package media

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RLS source: f84797726c6af8d64a228d9349832002c8f33888, MIT.
// Display views never feed identity or quality extraction. Explicit unbound and
// collection scopes only remove authority; no absolute mapping is invented.
var rlsNextAudioTester = regexp.MustCompile(`(?i)(?:^|[ ._-])Audio[ ._-]+Experience[ ._-]+Tester[ ._-]+DVDR(?:$|[ ._-])`)
var rlsNextLimitedDisc = regexp.MustCompile(`(?i)[ ._-]+Limited[ ._-]+Edition[ ._-]+DVDR[ ._-]+D[0-9]{1,2}-[A-Za-z][A-Za-z0-9]+$`)
var rlsNextSeason = regexp.MustCompile(`(?i)[ ._-]+S[0-9]{1,2}(?:E[0-9]{1,4}|D[0-9]{1,2}|DVD[0-9]{1,2})?(?:[ ._-]|$)`)
var rlsNextStandard = regexp.MustCompile(`(?i)[ ._-]+S[0-9]{1,2}E[0-9]{1,4}(?:[ ._-]|$)`)
var rlsNextRange = regexp.MustCompile(`(?i)(?:^|[ ._/\\|\[\]{}-])S[0-9]{1,2}[ ._-]*-[ ._-]*S[0-9]{1,2}[ ._-]+(?:COMPLETE|DKsubs)(?:$|[ ._/\\|\[\]{}-])`)
var rlsNextCross = regexp.MustCompile(`(?i)[ ._-]+S[0-9]{1,2}E[0-9]{1,4}_S[0-9]{1,2}E[0-9]{1,4}(?:$|[ ._-])`)
var rlsNextEpisodePack = regexp.MustCompile(`(?i)(?:^|[ ._/\\|\[\]{}-])Ep[ ._-]+[0-9]{1,3}-[0-9]{1,3}[ ._-]+Complete(?:$|[ ._/\\|\[\]{}-])`)
var rlsNextDiscs = regexp.MustCompile(`(?i)[ ._-]+(?:[2-9]|[1-9][0-9])x?DVD[59]?(?:[ ._-]+(?:NTSC|PAL|BOX|DUTCH[ ._-]+COVER))(?:$|[ ._-])`)
var rlsNextPassword = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]{1,40})\{\{[^{}\r\n]*\}\}$`)
var rlsNextWrappedGroup = regexp.MustCompile(`(?:[ ]-[ ]+|(?i:x265)[ ]+)([A-Za-z][A-Za-z0-9_-]{1,40})\)(?:\[TAoE\])?$`)
var rlsNextAudioGroup = regexp.MustCompile(`(?i)(?:AAC|AC3|EAC3)[ .]+[1-9][ .]1[ .]+([A-Za-z][A-Za-z0-9_-]{1,40})\)$`)
var rlsNextHyphenGroup = regexp.MustCompile(`-([A-Za-z][A-Za-z0-9_]*(?:-[A-Za-z0-9]+)*)$`)
var rlsNextInitials = regexp.MustCompile(`^[A-Za-z](?:_[A-Za-z]){1,7}$`)
var rlsNextCompoundGroup = regexp.MustCompile(`(?i)x264[ .]+([A-Za-z]-[A-Za-z0-9]{2,20})$`)
var rlsNextR5 = regexp.MustCompile(`(?i)[ ._-]+R5[ ._-]+Line[ ._-]+Dubbed[ ._-]+German[ ._-]+READ[ ._-]+NFO[ ._-]+XviD-`)
var rlsNextTS = regexp.MustCompile(`(?i)[ ._-]+TS[ ._-]+XViD[ ._-]+AC3[ ._-]+[A-Za-z][A-Za-z0-9-]*$`)
var rlsNextDanish = regexp.MustCompile(`(?i)[ ._-]+DANiSH[ ._-]+720p[ ._-]+HDTV`)
var rlsNextAbbreviation = regexp.MustCompile(`\b(Dr|Mr|Mrs)\.\.`)
var rlsNextUnbound = regexp.MustCompile(`(?i)[ ._-]+(?:E[0-9]{2,4}|Ep[ ._-]+[0-9]{2,4})(?:[ ._-]|$)`)
var rlsNextSpaced = regexp.MustCompile(`(?i)\bS([0-9]{1,2})[ .]+E([0-9]{1,4})\b`)
var rlsNextPadded = regexp.MustCompile(`(?i)\bS[0-9]{1,2}E0[0-9]{4,}\b`)
var rlsNextWord = regexp.MustCompile(`(?i)\bS([0-9]{1,2})[ ]+-[ ]+Episode[ ]+([0-9]{1,4})\b`)
var rlsNextBBC = regexp.MustCompile(`^BBC\.(.+?)\.(?:19|20)[0-9]{2}\.BluRay\.1080p\.DTS-HD\.MA\.5\.1\.AVC\.REMUX-[A-Za-z][A-Za-z0-9]+$`)
var rlsNextAbsoluteDisplay = regexp.MustCompile(`^\[(HorribleSubs|SubsPlease)\] ([^\[\]\r\n]+?) - [0-9]{1,4} (?:\[(?:480|720|1080|2160)p\]|\((?:480|720|1080|2160)p\) \[[0-9A-Fa-f]{8}\])\.mkv$`)
var rlsNextP9 = regexp.MustCompile(`^\[P9\][ ]+(.+?)[ ]+-[ ]+(?i:S[0-9]{1,2}E[0-9]{1,4})[ ]+-[ ]+[^\[\]]+[ ]+\(VRV WEB-DL 1080p AAC\)\.mkv$`)

func rlsNextSafe(raw string) bool {
	return !strings.ContainsAny(raw, "/\\\r\n") && !hasAmbiguousYear(raw)
}

func rlsNextScope(raw string) int {
	for _, re := range []*regexp.Regexp{rlsNextRange, rlsNextEpisodePack, rlsNextDiscs} {
		if at := re.FindStringIndex(raw); at != nil {

			return at[0]
		}
	}
	return -1
}

func rlsNextUnboundAt(raw string) int {

	if !titleQualityBoundaryRegex.MatchString(raw) || rlsNextScope(raw) >= 0 {
		return -1
	}
	for _, span := range rlsNextUnbound.FindAllStringIndex(raw, -1) {
		end := span[1]
		if end > span[0] && strings.ContainsAny(raw[end-1:end], " ._-") {
			end--
		}
		bound := false
		for _, re := range []*regexp.Regexp{multiEpRegex1, dashedEpisodeListRegex} {
			for _, canonical := range re.FindAllStringIndex(raw, -1) {
				if span[0] >= canonical[0] && end <= canonical[1] {
					bound = true
				}
			}
		}
		if !bound {
			return span[0]
		}
	}
	return -1
}

func rlsNextUnsupported(raw string) bool {
	return rlsNextSpaced.MatchString(raw) || rlsNextPadded.MatchString(raw) || rlsNextWord.MatchString(raw)
}

func rlsNextTitle(raw, title string, year int) string {
	if !rlsNextSafe(raw) {
		return title
	}

	if at := rlsNextLimitedDisc.FindStringIndex(raw); at != nil {
		return cleanTitle(raw[:at[0]])
	}
	if m := rlsNextBBC.FindStringSubmatch(raw); m != nil {
		return normalizeTitleSeparators(m[1])
	}

	if m := rlsNextP9.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	for _, re := range []*regexp.Regexp{rlsNextR5, rlsNextTS} {
		if at := re.FindStringIndex(raw); at != nil {
			return recognizeFilenameEvidence(raw[:at[0]]).Title
		}
	}
	if at := rlsNextDanish.FindStringIndex(raw); at != nil {
		return recognizeFilenameEvidence(raw[:at[0]]).Title
	}
	if at := rlsNextScope(raw); at >= 0 {
		return recognizeFilenameEvidence(raw[:at]).Title
	}
	if at := rlsNextCross.FindStringIndex(raw); at != nil {
		return cleanTitle(raw[:at[0]])
	}
	if at := rlsNextUnboundAt(raw); at >= 0 {
		return cleanTitle(raw[:at])
	}
	if year > 0 {
		if at := yearRegex.FindStringIndex(raw); at != nil && titleQualityBoundaryRegex.MatchString(raw[at[1]:]) {
			prefix := strings.Trim(raw[:at[0]], " ._-")
			if rlsNextAbbreviation.MatchString(prefix) && !strings.Contains(prefix, "§") {
				// Preserve abbreviation stops explicitly encoded by a doubled separator.
				prefix = rlsNextAbbreviation.ReplaceAllString(prefix, "${1}§ ")
				return strings.ReplaceAll(normalizeTitleSeparators(prefix), "§", ".")
			}
			// Spaces plus comma punctuation distinguish a prose title from dotted scene separators.
			if strings.Contains(prefix, ", ") && strings.Contains(prefix, "...") {
				return prefix
			}
		}
	}
	return strings.Join(strings.Fields(title), " ")
}

func rlsNextGroup(raw, group string) string {
	if !rlsNextSafe(raw) {
		return group
	}
	name := strings.TrimSpace(raw)
	for _, ext := range []string{".torrent", ".mkv", ".mp4", ".avi"} {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	if match := rlsNextPassword.FindStringSubmatch(group); match != nil {
		return match[1]
	}
	if !titleQualityBoundaryRegex.MatchString(name) {
		return group
	}
	if group == "" && strings.HasSuffix(name, ")") {
		q := extractQuality(raw)
		if q.Resolution != "" && q.Source != "" && q.Codec != "" {
			for _, re := range []*regexp.Regexp{rlsNextWrappedGroup, rlsNextAudioGroup} {
				if match := re.FindStringSubmatch(name); match != nil && technicalGroupName(match[1]) {
					return match[1]
				}
			}
		}
	}
	if match := rlsNextCompoundGroup.FindStringSubmatch(name); match != nil {
		return match[1]
	}
	if group == "" {
		if match := rlsNextHyphenGroup.FindStringSubmatch(name); match != nil {
			if strings.Contains(match[1], "_") && !technicalGroupCodec.MatchString(match[1]) && !rlsNextInitials.MatchString(match[1]) && !strings.HasSuffix(strings.ToLower(match[1]), "_int") {
				return match[1]
			}
		}
	}
	return group
}

func rlsNextRelease(raw string, res EpisodeRecognition) EpisodeRecognition {
	if strings.Contains(res.Reason, "conflicting") || res.Reason == "ambiguous_media_year" || res.Reason == "non_video_file" || res.Reason == "episode_coverage_unresolved" {
		return res
	}
	if rlsNextUnsupported(raw) {
		res.Season, res.AbsoluteEpisode = 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.Confidence, res.RequiresReview = 0, true
	}
	if rlsNextScope(raw) >= 0 {
		res.Season, res.AbsoluteEpisode = 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview, res.Confidence = false, true, 0
		if res.Reason != "non_video_file" {
			res.Reason = "multi_unit_collection"
		}
	}
	if rlsNextUnboundAt(raw) >= 0 {
		res.MediaType, res.Season, res.AbsoluteEpisode = TypeTV, 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview, res.Confidence = false, true, 0
		if res.Reason != "non_video_file" {
			res.Reason = "absolute_mapping_required"
		}
		res.Year, _ = recognizeYear(raw)
	}
	if res.Reason == "invalid_episode_coverage" && res.Confidence == 0 && res.RequiresReview && len(res.Episodes) == 0 && len(res.UnitKeys) == 0 {
		res.Season, res.AbsoluteEpisode = 0, 0
	}
	if rlsNextAudioTester.MatchString(raw) {
		res.Season, res.AbsoluteEpisode = 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview, res.Confidence = false, true, 0
		if res.Reason != "non_video_file" {
			res.Reason = "partial_movie_content"
		}
	}
	if len(res.Episodes) > 1 && res.Reason == "explicit_episode_list" && !sort.IntsAreSorted(res.Episodes) {
		sort.Ints(res.Episodes)
		res.UnitKeys = make([]string, len(res.Episodes))
		for i, ep := range res.Episodes {
			res.UnitKeys[i] = fmt.Sprintf("S%02dE%02d", res.Season, ep)
		}
	}
	res.Title = rlsNextTitle(raw, res.Title, res.Year)
	res.Quality.Group = rlsNextGroup(raw, res.Quality.Group)
	return res
}

func rlsNextMovie(raw string, res Recognition) Recognition {
	if res.Confidence == 0 && res.RequiresReview && res.Title == "" {
		return res
	}
	if rlsNextScope(raw) < 0 && (len(standardEpRegex.FindAllStringIndex(raw, -1)) > 1 || (standardEpRegex.MatchString(raw) && altEpRegex.MatchString(raw)) || (res.Confidence == 0 && res.RequiresReview && partialMovieSuffix(raw))) {
		return res
	}

	if rlsNextUnsupported(raw) || rlsNextScope(raw) >= 0 || rlsNextUnboundAt(raw) >= 0 || rlsNextAudioTester.MatchString(raw) {
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
	}
	if !rlsNextSafe(raw) {
		return res
	}
	normalized, numbered := raw, rlsNextUnsupported(raw)
	if numbered || (!strings.HasPrefix(raw, "[") && (rlsNextSeason.MatchString(raw) || chainEpRegex.MatchString(raw))) || rlsNextScope(raw) >= 0 || rlsNextUnboundAt(raw) >= 0 || rlsNextAudioTester.MatchString(raw) {
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true

		if !strings.HasPrefix(raw, "[") {
			for _, re := range []*regexp.Regexp{rlsNextStandard, chainEpRegex} {
				if at := re.FindStringIndex(normalized); at != nil {
					index := at[0]
					if re == rlsNextStandard {
						index += strings.IndexAny(normalized[at[0]:at[1]], "Ss")
					}
					res.Title = stripSeriesReleaseYear(cleanTitle(normalized[:index]), res.Year)
					break
				}
			}
		}
	}
	if at := rlsDiscScope(raw); at >= 0 {
		res.Title = recognizeFilenameEvidence(raw[:at]).Title
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
	}
	if m := rlsNextAbsoluteDisplay.FindStringSubmatch(raw); m != nil && !competingCoverageRegex.MatchString(m[2]) && !extraAbsoluteCoverageRegex.MatchString(m[2]) && !yearRegex.MatchString(m[2]) {
		res.Title = cleanTitle(m[2])
		res.Quality.Group = m[1]
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
	}
	if rlsNextP9.MatchString(raw) {
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
	}
	res.Title = rlsNextTitle(raw, res.Title, res.Year)
	res.Quality.Group = rlsNextGroup(raw, res.Quality.Group)
	return res
}
