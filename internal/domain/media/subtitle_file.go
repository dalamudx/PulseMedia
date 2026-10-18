package media

import (
	"path/filepath"
	"strings"
)

// Subtitle, text-sidecar and ZIP archive suffixes describe non-video files.
// Remove only these explicit suffixes for a lexical title, retaining raw input.
func subtitleFileTitleView(name string) (string, bool) {
	view := strings.TrimSpace(name)
	found := false
	for {
		ext := filepath.Ext(view)
		switch strings.ToLower(ext) {
		case ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".txt", ".nfo", ".zip":
			view = strings.TrimSuffix(view, ext)
			found = true
		default:
			return view, found
		}
	}
}
