package naming

import (
	"strings"
	"unicode"
)

var videoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".ts": true, ".m2ts": true, ".iso": true, ".wmv": true,
}

const DefaultMinMediaBytes int64 = 200 * 1024 * 1024

// IsPrimaryMediaPath classifies a path without claiming a size or file identity.
// Callers supply the path relative to their trusted root, not the root's name.
func IsPrimaryMediaPath(filePath string) (bool, string) {
	normalized := strings.ReplaceAll(filePath, "\\", "/")
	if !IsVideoPath(normalized) {
		return false, "non_video_extension"
	}
	if strings.ContainsAny(normalized, "\x00\r\n") {
		return false, "invalid_media_path"
	}
	for _, component := range strings.Split(normalized, "/") {
		if strings.HasPrefix(component, ".") {
			return false, "hidden_or_invalid_path"
		}
		for _, token := range strings.FieldsFunc(strings.ToLower(component), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
			switch token {
			case "sample", "samples", "trailer", "trailers", "preview", "previews", "featurette", "featurettes", "样片", "预告", "预告片", "花絮":
				return false, "sample_or_trailer"
			}
		}
	}
	return true, "eligible"
}

// IsEligibleMediaFile requires measured, positive bytes in addition to path eligibility.
// Zero minimum selects the default; an explicit positive minimum is inclusive.
func IsEligibleMediaFile(filePath string, fileSizeBytes, minSizeBytes int64) (bool, string) {
	if ok, reason := IsPrimaryMediaPath(filePath); !ok {
		return false, reason
	}
	if fileSizeBytes <= 0 {
		return false, "invalid_file_size"
	}
	if minSizeBytes < 0 {
		return false, "invalid_size_threshold"
	}
	if minSizeBytes == 0 {
		minSizeBytes = DefaultMinMediaBytes
	}
	if fileSizeBytes < minSizeBytes {
		return false, "below_size_threshold"
	}
	return true, "eligible"
}
