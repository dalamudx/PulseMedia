package media

import (
	"regexp"
	"strings"
	"unicode"
)

// Private repair proposal. Raw evidence only; never feed these title views
// back into quality, identity, year, or coverage inference.
var scoutRepairTVComplete = regexp.MustCompile(`(?i)^(.+?)[ ._]+(?:S[0-9]{1,2}E[0-9]{1,4}|Season[ ._]*[0-9]{1,2})[ ._]+Complete[ ._]+(?:480p|720p|1080p|2160p)[ ._]+(?:HDTV|BRrip|BluRay|WEB-DL|WEBRip)(?:[ ._]+(?:x264|x265|h264))?(?:-[a-z][a-z0-9]*|[ ._]+[a-z]+(?:[ ._]+\([a-z]+\))?)?(?:\.(?:mkv|avi|mp4))?$`)
var scoutRepairXvid = regexp.MustCompile(`(?i)^(.+?)[ ._]+xvid-([a-z][a-z0-9]*)\.(?:avi|mkv|mp4)$`)
var scoutRepairDirectors = regexp.MustCompile(`(?i)^(.+?)[ ._]+DIRECTORS[ ._]+CUT[ ._]+((?:19|20)[0-9]{2})[ ._]+(?:480p|720p|1080p|2160p)[ ._]+(?:BRrip|BluRay|BDRip)[ ._]+(?:x264|h264)(?:[ ._]+[a-z]+)?\.(?:avi|mkv|mp4)$`)

// A compact SeasonN COMPLETE declaration cannot coexist with a standard
// episode token. Inspect the complete raw path so a slash cannot hide it.
var scoutRepairCompactSeasonComplete = regexp.MustCompile(`(?i)(?:^|[ ._/\\])Season[0-9]{1,2}[ ._]+Complete(?:$|[ ._/\\])`)

func scoutRepairMixedComplete(raw string) bool {
	return scoutRepairCompactSeasonComplete.MatchString(raw) && (standardEpRegex.MatchString(raw) || altEpRegex.MatchString(raw))
}

func scoutRepairPlainTitle(raw, title string) bool {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "/\\\r\n[]{}") || !strings.ContainsFunc(title, unicode.IsLetter) {
		return false
	}
	// A second numbering scheme, date, or identity stays with the existing guards.
	return !ambiguousEpisodeRegex.MatchString(title) && !competingCoverageRegex.MatchString(title) && len(calendarFilenameSpans(title)) == 0 && !hasAmbiguousYear(raw)
}

func scoutRepairCompleteTitle(raw string) (string, bool) {
	m := scoutRepairTVComplete.FindStringSubmatch(raw)
	if m == nil || !scoutRepairPlainTitle(raw, m[1]) {
		return "", false
	}
	return cleanTitle(m[1]), true
}

func scoutRepairTechnicalTitle(raw string) (string, bool) {
	for _, pattern := range []*regexp.Regexp{scoutRepairXvid, scoutRepairDirectors} {
		if m := pattern.FindStringSubmatch(raw); m != nil && scoutRepairPlainTitle(raw, m[1]) {
			if year, ambiguous := recognizeYear(m[1]); year != 0 || ambiguous {
				continue
			}
			return cleanTitle(m[1]), true
		}
	}
	return "", false
}

func scoutRepairRelease(raw string, res EpisodeRecognition) EpisodeRecognition {
	if scoutRepairMixedComplete(raw) {
		res.MediaType, res.Season, res.AbsoluteEpisode = TypeTV, 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview = false, true
		res.Confidence, res.Reason = 0, "conflicting_episode_coverage"
		return res
	}
	if title, ok := scoutRepairCompleteTitle(raw); ok {
		res.Title, res.MediaType = title, TypeTV
		res.Season, res.AbsoluteEpisode = 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview = false, true
		res.Confidence, res.Reason = 0, "multi_unit_collection"
	} else if title, ok := scoutRepairTechnicalTitle(raw); ok && res.MediaType == TypeMovie && res.Reason != "ambiguous_media_year" {
		res.Title = title
	}
	return res
}

func scoutRepairFilename(raw string, res Recognition) Recognition {
	if scoutRepairMixedComplete(raw) {
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
		return res
	}
	if title, ok := scoutRepairCompleteTitle(raw); ok {
		res.Title, res.Identity = title, nil
		res.Confidence, res.RequiresReview = 0, true
	} else if title, ok := scoutRepairTechnicalTitle(raw); ok {
		res.Title = title
	}
	return res
}
