package media

import "regexp"

// A split archive volume cannot certify complete media coverage. Inspect the
// entire original release string so a basename or external ID cannot hide it.
var archiveVolumeEvidence = regexp.MustCompile(`(?i)\.part[0-9]+\.rar(?:$|[/\\])`)

func archiveScopeRefusalRelease(raw string, res EpisodeRecognition) EpisodeRecognition {
	if !archiveVolumeEvidence.MatchString(raw) {
		return res
	}
	res.Season, res.AbsoluteEpisode = 0, 0
	res.Episodes, res.UnitKeys = nil, nil
	res.IsAnime, res.RequiresReview, res.Confidence = false, true, 0
	if res.Reason != "non_video_file" {
		res.Reason = "partial_movie_content"
	}
	return res
}

func archiveScopeRefusalMovie(raw string, res Recognition) Recognition {
	if archiveVolumeEvidence.MatchString(raw) {
		res.Identity, res.Confidence, res.RequiresReview = nil, 0, true
	}
	return res
}
