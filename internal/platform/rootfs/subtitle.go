package rootfs

import (
	"path"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.MediaSubtitleFilesystem = (*Filesystem)(nil)

func subtitleVideo(relative string) bool {
	switch strings.ToLower(path.Ext(relative)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".ts", ".m2ts", ".webm", ".wmv", ".mpg", ".mpeg":
		return true
	}
	return false
}

func validSubtitleRequest(req SidecarRequest) bool {
	return subtitleVideo(req.SourceRelative) &&
		req.DestinationDirectory == path.Dir(req.SourceRelative) &&
		req.Name == strings.TrimSuffix(path.Base(req.SourceRelative), path.Ext(req.SourceRelative))+".zh-cn.default.srt" &&
		len(req.Content) > 0 && len(req.Content) <= ports.MaxSubtitleBytes
}
