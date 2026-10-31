package media

import (
	"errors"
	"path"
	"strings"
)

var ErrTVEpisodeUpgradeTorrentFilesReview = errors.New("TV episode upgrade torrent files require review; protection retained")

// TVEpisodeUpgradeTarget binds file evidence to one current canonical episode.
// Quality, old-file exclusivity and execution authority require separate checks.
type TVEpisodeUpgradeTarget = AutomaticTVTorrentTarget

// ValidateTVEpisodeUpgradeTorrentFiles requires one positive standard episode video
// with exact catalog identity. Parent directories cannot supply missing identity.
func ValidateTVEpisodeUpgradeTorrentFiles(files []TVTorrentFile, target TVEpisodeUpgradeTarget) (*TVTorrentUnitFile, error) {
	video, err := ValidateAutomaticTVSingleEpisodeFiles(files, target)
	if err != nil {
		return nil, ErrTVEpisodeUpgradeTorrentFilesReview
	}
	stem := strings.TrimSuffix(path.Base(video.Path), path.Ext(video.Path))
	for _, f := range files {
		if f.Path == video.Path {
			continue
		}
		name := path.Base(f.Path)
		if !automaticTVIdentitySafeText(name) || !tvEpisodeUpgradeSidecarMatches(name, stem) {
			return nil, ErrTVEpisodeUpgradeTorrentFilesReview
		}
	}
	return video, nil
}

func tvEpisodeUpgradeSidecarMatches(name, videoStem string) bool {
	ext := strings.ToLower(path.Ext(name))
	stem := strings.TrimSuffix(name, path.Ext(name))
	// Conventional artwork and readme names carry no alternative episode claim.
	switch strings.ToLower(name) {
	case "poster.jpg", "poster.jpeg", "poster.png", "folder.jpg", "folder.png", "fanart.jpg", "fanart.png", "readme.txt":
		return true
	}
	if ext == ".srt" || ext == ".ass" || ext == ".ssa" || ext == ".sub" || ext == ".idx" {
		// Only explicit language/accessibility tags may follow the exact video stem.
		for !strings.EqualFold(stem, videoStem) {
			i := strings.LastIndexByte(stem, '.')
			if i < 0 {
				return false
			}
			switch strings.ToLower(stem[i+1:]) {
			case "en", "eng", "zh", "zho", "chi", "chs", "cht", "zh-cn", "zh-tw", "default", "forced", "sdh":
				stem = stem[:i]
			default:
				return false
			}
		}
	}
	return strings.EqualFold(stem, videoStem)
}
