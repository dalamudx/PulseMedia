package media

import (
	"errors"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrTVCoverageFilesRequireReview = errors.New("torrent file coverage requires manual review")

type TVTorrentFile struct {
	Path string
	Size int64
}

type TVTorrentUnitFile struct {
	Path string
	Size int64
	Unit NumberingRow
}

// ValidateCompleteTVTorrentFiles accepts one explicitly numbered video per frozen
// unit. It never fills ranges, guesses a season or grants authority to sidecars.
func ValidateCompleteTVTorrentFiles(files []TVTorrentFile, approved []NumberingRow, canonical []NumberingUnit) ([]TVTorrentUnitFile, error) {
	fail := ErrTVCoverageFilesRequireReview
	if len(approved) < 2 || len(approved) > 1000 || len(files) < 2 || len(files) > 4096 {
		return nil, fail
	}
	season := 0
	for _, u := range canonical {
		if !u.Retired && u.ID == approved[0].UnitID {
			season = u.Season
		}
	}
	if season < 1 || season > 999 {
		return nil, fail
	}
	prefix := FormatEpisodeUnitKey(season, 1)
	prefix = prefix[:strings.IndexByte(prefix, 'E')+1]
	current := map[string]NumberingUnit{}
	ids := map[uuid.UUID]bool{}
	providers := map[int64]bool{}
	for _, u := range canonical {
		if u.Retired || (u.Season != season && !strings.HasPrefix(u.Key, prefix)) {
			continue
		}
		if u.Season != season || u.Episode < 1 || u.Key != FormatEpisodeUnitKey(season, u.Episode) || u.ID == uuid.Nil || u.Version < 1 || u.EpisodeID < 1 || current[u.Key].ID != uuid.Nil || ids[u.ID] || providers[u.EpisodeID] {
			return nil, fail
		}
		current[u.Key] = u
		ids[u.ID] = true
		providers[u.EpisodeID] = true
	}
	if len(current) != len(approved) {
		return nil, fail
	}
	frozen := map[string]NumberingRow{}
	for _, r := range approved {
		u, exists := current[r.UnitKey]
		if !exists || r.UnitID != u.ID || r.UnitVersion != u.Version || r.EpisodeID != u.EpisodeID || r.SourceNumber != u.Episode || frozen[r.UnitKey].UnitID != uuid.Nil {
			return nil, fail
		}
		frozen[r.UnitKey] = r
	}
	used := map[string]bool{}
	paths := map[string]bool{}
	result := make([]TVTorrentUnitFile, 0, len(approved))
	for _, f := range files {
		if f.Path == "" || !utf8.ValidString(f.Path) || len(f.Path) > 4096 || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || f.Path == ".." || strings.HasPrefix(f.Path, "../") || strings.ContainsAny(f.Path, "\\:") || f.Size < 0 || paths[strings.ToLower(f.Path)] {
			return nil, fail
		}
		for _, r := range f.Path {
			if r < 32 || r == 127 {
				return nil, fail
			}
		}
		paths[strings.ToLower(f.Path)] = true
		switch strings.ToLower(path.Ext(f.Path)) {
		case ".srt", ".ass", ".ssa", ".sub", ".idx", ".nfo", ".txt", ".jpg", ".jpeg", ".png":
			continue
		case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".webm", ".ts", ".m2ts":
		default:
			return nil, fail
		}
		if f.Size < 1 {
			return nil, fail
		}
		rows, _ := ResolveStandardCoverage(RecognizeRelease(path.Base(f.Path)), canonical)
		if len(rows) != 1 {
			return nil, fail
		}
		row := rows[0]
		want, exists := frozen[row.UnitKey]
		if !exists || used[row.UnitKey] || row != want {
			return nil, fail
		}
		for _, directory := range strings.Split(path.Dir(f.Path), "/") {
			if partialEpisodeSuffixRegex.MatchString(directory) {
				return nil, fail
			}
			parsed := RecognizeRelease(directory)
			switch parsed.Reason {
			case "multi_unit_collection", "non_video_file", "partial_movie_content", "partial_episode_content", "uncertain_pack_or_special":
				return nil, fail
			}
			if parsed.MediaType != TypeTV {
				continue
			}
			parentRows, _ := ResolveStandardCoverage(parsed, canonical)
			if len(parentRows) == 0 {
				return nil, fail
			}
			contains := false
			for _, parentRow := range parentRows {
				if frozen[parentRow.UnitKey] != parentRow {
					return nil, fail
				}
				contains = contains || parentRow == want
			}
			if !contains {
				return nil, fail
			}
		}
		used[row.UnitKey] = true
		result = append(result, TVTorrentUnitFile{Path: f.Path, Size: f.Size, Unit: want})
	}
	for p := range paths {
		for parent := strings.LastIndexByte(p, '/'); parent >= 0; parent = strings.LastIndexByte(p[:parent], '/') {
			if paths[p[:parent]] {
				return nil, fail
			}
		}
	}
	if len(result) != len(approved) {
		return nil, fail
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Unit.UnitKey < result[j].Unit.UnitKey })
	return result, nil
}
