package media

import (
	"regexp"
	"strings"
)

// Refusal evidence is scanned across the complete original name, before any
// slash-based display view. This is deliberately not executable numbering.
var virenSeasonScope = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:Season[ ._]+[0-9]{1,2}[ ._-]+[0-9]{1,3}|[0-9]{1,2}(?:st|nd|rd|th)[ ._]+Season[ ._]+-[ ._]+[0-9]{1,3})(?:$|[^a-z0-9])`)
var virenStandardCompetitor = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])S[0-9]{1,2}E[0-9]{1,4}(?:$|[^a-z0-9])`)

func virenRepairRelease(raw string, res EpisodeRecognition) EpisodeRecognition {
	if res.Reason == "non_video_file" {
		return res
	}
	res.Title = virenLexicalTitle(raw, res.Title, res.Year)
	if title, group := virenAnimeTitle(raw); title != "" {
		res.Title = title
		// The complete certified layout distinguishes the publisher from CRC/episode tails.
		res.Quality.Group = group
	}
	if title := virenBracketTitle(raw); title != "" {
		res.Title = title
		// Independent Viren full-main literal 224-235 certifies this publisher.
		if strings.HasPrefix(raw, "[GM-Team]") {
			res.Quality.Group = "GM-Team"
		}
	}
	if virenSeasonScope.MatchString(raw) {
		res.MediaType = TypeTV
		res.Year = 0
		res.Season = 0
		res.Episodes = nil
		res.UnitKeys = nil
		res.AbsoluteEpisode = 0
		res.IsAnime = false
		res.Confidence = 0
		res.RequiresReview = true
		if strings.Contains(res.Reason, "conflicting") || virenStandardCompetitor.MatchString(raw) {
			res.Reason = "conflicting_episode_coverage"
		} else {
			res.Reason = "episode_coverage_unresolved"
		}
	}
	return res
}
func virenRepairMovie(raw string, res Recognition) Recognition {
	if _, nonVideo := subtitleFileTitleView(raw); nonVideo {
		return res
	}
	res.Title = virenLexicalTitle(raw, res.Title, res.Year)
	if title, group := virenAnimeTitle(raw); title != "" {
		res.Title = title
		// The complete certified layout distinguishes the publisher from CRC/episode tails.
		res.Quality.Group = group
	}
	if title := virenBracketTitle(raw); title != "" {
		res.Title = title
		// Independent Viren full-main literal 224-235 certifies this publisher.
		if strings.HasPrefix(raw, "[GM-Team]") {
			res.Quality.Group = "GM-Team"
		}
	}
	if virenSeasonScope.MatchString(raw) {
		res.Year = 0
		res.Identity = nil
		res.Confidence = 0
		res.RequiresReview = true
	}
	return res
}
