package media

import (
	"errors"
	"math"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrAutomaticTVTorrentFilesReview = errors.New("automatic TV torrent files require review; protection retained")

var automaticTVTorrentPartRegex = regexp.MustCompile(`(?i)\b(?:part|pt)[ ._-]*(?:[0-9]+|[ivx]+|one|two|three)\b`)

type AutomaticTVTorrentTarget struct {
	Unit                 NumberingUnit
	Title, OriginalTitle string
	Year                 int32
	TMDBID               int64
}

// ValidateAutomaticTVSingleEpisodeFiles proves only one standard episode's file
// binding. Current aired/Wanted authority and permission to send or ingest are separate.
func ValidateAutomaticTVSingleEpisodeFiles(files []TVTorrentFile, target AutomaticTVTorrentTarget) (*TVTorrentUnitFile, error) {
	fail := ErrAutomaticTVTorrentFilesReview
	u := target.Unit
	if len(files) < 1 || len(files) > 4096 || u.ID == uuid.Nil || u.Version < 1 || u.EpisodeID < 1 ||
		u.Retired || u.Season < 1 || u.Season > 999 || u.Episode < 1 || u.Episode > 9999 ||
		u.Key != FormatEpisodeUnitKey(u.Season, u.Episode) || target.TMDBID < 1 || target.Year < 1 || target.Year > 9999 {
		return nil, fail
	}
	want := NumberingRow{SourceNumber: u.Episode, EpisodeID: u.EpisodeID, UnitID: u.ID, UnitKey: u.Key, UnitVersion: u.Version}
	paths := make(map[string]bool, len(files))
	var result *TVTorrentUnitFile
	var total int64
	for _, f := range files {
		p := f.Path
		folded := automaticTVTorrentPathFold(p)
		if p == "" || p == "." || !utf8.ValidString(p) || len(p) > 4096 || path.IsAbs(p) || path.Clean(p) != p ||
			p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\:") || paths[folded] ||
			f.Size < 0 || f.Size > math.MaxInt64-total {
			return nil, fail
		}
		for _, r := range p {
			if unicode.IsControl(r) {
				return nil, fail
			}
		}
		total += f.Size
		paths[folded] = true
		for _, parent := range strings.Split(path.Dir(p), "/") {
			if parent != "." && !automaticTVTorrentParentMatches(parent, target, want) {
				return nil, fail
			}
		}
		switch strings.ToLower(path.Ext(p)) {
		case ".srt", ".ass", ".ssa", ".sub", ".idx", ".nfo", ".txt", ".jpg", ".jpeg", ".png":
			continue
		case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".webm", ".ts", ".m2ts":
		default:
			return nil, fail
		}
		name := path.Base(p)
		parsed := RecognizeRelease(name)
		rows, _ := ResolveStandardSingleEpisode(parsed, []NumberingUnit{u})
		if result != nil || f.Size < 1 || automaticTVTorrentPartRegex.MatchString(name) || parsed.Reason != "standard_season_episode" ||
			len(rows) != 1 || rows[0] != want ||
			DecideAutomaticTVIdentity(name, target.Title, target.OriginalTitle, int(target.Year), target.TMDBID).State != "eligible" {
			return nil, fail
		}
		result = &TVTorrentUnitFile{Path: p, Size: f.Size, Unit: want}
	}
	for p := range paths {
		for i := strings.LastIndexByte(p, '/'); i >= 0; i = strings.LastIndexByte(p[:i], '/') {
			if paths[p[:i]] {
				return nil, fail
			}
		}
	}
	if result == nil {
		return nil, fail
	}
	return result, nil
}

// Use Unicode simple folding so equivalent paths such as s and long-s collide.
func automaticTVTorrentPathFold(s string) string {
	return strings.Map(func(r rune) rune {
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		return minimum
	}, s)
}

func automaticTVTorrentParentMatches(name string, target AutomaticTVTorrentTarget, want NumberingRow) bool {
	if !automaticTVIdentitySafeText(name) || HasConflictingTVIdentity(name, target.TMDBID) || imdbIDRegex.MatchString(name) || partialEpisodeSuffixRegex.MatchString(name) || automaticTVTorrentPartRegex.MatchString(name) {
		return false
	}
	parsed := RecognizeRelease(name)
	switch parsed.Reason {
	case "multi_unit_collection", "non_video_file", "partial_movie_content", "partial_episode_content", "uncertain_pack_or_special", "ambiguous_media_year":
		return false
	}
	if parsed.MediaType == TypeTV || standardEpRegex.MatchString(name) || altEpRegex.MatchString(name) || absoluteEpisodeHintRegex.MatchString(name) {
		rows, _ := ResolveStandardSingleEpisode(parsed, []NumberingUnit{target.Unit})
		if parsed.Reason != "standard_season_episode" || len(rows) != 1 || rows[0] != want {
			return false
		}
		return DecideAutomaticTVIdentity(name, target.Title, target.OriginalTitle, int(target.Year), target.TMDBID).State == "eligible"
	}
	year, ambiguous := recognizeYear(name)
	if ambiguous || (year > 0 && year != int(target.Year)) {
		return false
	}
	key := normalizeMovieTitle(parsed.Title)
	return key != "" && (key == normalizeMovieTitle(target.Title) || key == normalizeMovieTitle(target.OriginalTitle))
}
