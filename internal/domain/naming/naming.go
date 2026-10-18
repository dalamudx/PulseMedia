package naming

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// FormatMovieFile returns a clean, standardized movie file name based on title, year, and quality attributes.
func FormatMovieFile(title string, year int, q quality.Profile, ext string) string {
	title = sanitizeFileName(title)
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}

	tags := make([]string, 0, 3)
	if q.Source != "" {
		tags = append(tags, string(q.Source))
	}
	if q.Resolution != "" {
		tags = append(tags, string(q.Resolution))
	}
	if q.Codec != "" {
		tags = append(tags, string(q.Codec))
	}

	var tagStr string
	if len(tags) > 0 {
		tagStr = " [" + strings.Join(tags, " ") + "]"
	}

	if year > 0 {
		return fmt.Sprintf("%s (%d)%s%s", title, year, tagStr, ext)
	}
	return fmt.Sprintf("%s%s%s", title, tagStr, ext)
}

// FormatMovieDir returns the folder name for a movie release.
func FormatMovieDir(title string, year int) string {
	title = sanitizeFileName(title)
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

// CleanRelPath cleans relative paths and rejects directory traversal.
func CleanRelPath(relPath string) (string, error) {
	if strings.Contains(relPath, ":") {
		return "", fmt.Errorf("path escapes root: contains colon %s", relPath)
	}

	normalized := strings.ReplaceAll(relPath, "\\", "/")
	cleaned := filepath.Clean(normalized)

	if strings.HasPrefix(cleaned, "../") || cleaned == ".." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("path escapes root: %s", relPath)
	}

	parts := strings.Split(cleaned, "/")
	for _, part := range parts {
		if part == ".." || strings.HasPrefix(part, "..") {
			return "", fmt.Errorf("path escapes root: invalid segment %s", part)
		}
	}

	return cleaned, nil
}

func sanitizeFileName(name string) string {
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := name
	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "")
	}
	return strings.TrimSpace(result)
}
