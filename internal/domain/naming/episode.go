package naming

import (
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"path/filepath"
	"strings"
)

// FormatEpisodeRel formats only explicit canonical season/episode values; it never guesses numbering.
func FormatEpisodeRel(title string, year, season, episode int, profile quality.Profile, ext string) (string, error) {
	if strings.ContainsAny(title, "\x00\r\n") {
		return "", fmt.Errorf("invalid canonical episode naming input")
	}
	title = sanitizeFileName(title)
	if title == "" || title == "." || title == ".." || year < 0 || season < 0 || episode < 1 || season > 999 || episode > 9999 || strings.ContainsAny(title, "\x00\r\n") {
		return "", fmt.Errorf("invalid canonical episode naming input")
	}
	if ext != ".mkv" && ext != ".mp4" && ext != ".avi" && ext != ".ts" && ext != ".m2ts" && ext != ".mov" && ext != ".wmv" {
		return "", fmt.Errorf("unsupported episode file extension")
	}
	key := fmt.Sprintf("S%02dE%02d", season, episode)
	return CleanRelPath(filepath.Join(FormatMovieDir(title, year), fmt.Sprintf("Season %02d", season), FormatMovieFile(title+" - "+key, 0, profile, ext)))
}
