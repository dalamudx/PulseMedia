package naming

import (
	"path/filepath"
	"strings"
)

var videoExtensions = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".avi":  true,
	".ts":   true,
	".m2ts": true,
	".iso":  true,
	".wmv":  true,
}

var sampleKeywords = []string{
	"sample",
	"trailer",
	"preview",
	"featurette",
}

// IsEligibleMediaFile checks whether a file is a valid primary movie/TV video file,
// filtering out samples, trailers, text files, and tiny clips below size threshold.
func IsEligibleMediaFile(filePath string, fileSizeBytes, minSizeBytes int64) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	if !videoExtensions[ext] {
		return false, "non_video_extension"
	}

	baseLower := strings.ToLower(filepath.Base(filePath))
	for _, kw := range sampleKeywords {
		if strings.Contains(baseLower, kw) {
			return false, "sample_or_trailer"
		}
	}

	if minSizeBytes <= 0 {
		minSizeBytes = 200 * 1024 * 1024 // default 200MB
	}
	if fileSizeBytes > 0 && fileSizeBytes < minSizeBytes {
		return false, "below_size_threshold"
	}

	return true, "eligible"
}
