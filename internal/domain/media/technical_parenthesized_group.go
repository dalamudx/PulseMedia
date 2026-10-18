package media

import (
	"regexp"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

var (
	technicalGroupToken      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{1,31}$`)
	technicalGroupResolution = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:480|576|720|1080|2160|4320)p(?:[^a-z0-9]|$)`)
	technicalGroupSource     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:blu[ .-]?ray|bdrip|brrip|web[ .-]?dl|webrip|hdtv)(?:[^a-z0-9]|$)`)
	technicalGroupCodec      = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:x26[45]|h[ .]?26[45]|hevc|avc|av1)(?:[^a-z0-9]|$)`)
)

// technicalParenthesizedGroup reads only a complete trailing technical block.
// An outer group tag is distinct from the encoder explicitly named inside it.
// Ordinary title parentheses and unknown continuations remain untouched.
func technicalParenthesizedGroup(name string) string {
	name = strings.TrimSpace(name)
	outer := ""
	if strings.HasSuffix(name, "]") {
		open := strings.LastIndexByte(name, '[')
		if open < 0 {
			return ""
		}
		outer = name[open+1 : len(name)-1]
		if !technicalGroupName(outer) {
			return ""
		}
		name = strings.TrimSpace(name[:open])
	}
	if !strings.HasSuffix(name, ")") {
		return ""
	}
	open := strings.LastIndexByte(name, '(')
	if open < 0 {
		return ""
	}
	block := name[open+1 : len(name)-1]
	if strings.ContainsAny(block, "()[]") {
		return ""
	}
	dash := quality.ReleaseGroupDelimiter(block)
	if dash < 0 {
		return ""
	}
	inner := strings.TrimSpace(block[dash+1:])
	if !technicalGroupName(inner) {
		return ""
	}
	evidence := block[:dash]
	if !technicalGroupResolution.MatchString(evidence) || !technicalGroupSource.MatchString(evidence) || !technicalGroupCodec.MatchString(evidence) {
		return ""
	}
	if outer != "" {
		return outer
	}
	return inner
}

func technicalGroupName(name string) bool {
	if !technicalGroupToken.MatchString(name) || coverageGroupTailRegex.MatchString(name) || nonGroupMetadataTailRegex.MatchString(name) {
		return false
	}
	switch strings.ToLower(name) {
	case "imax", "extended", "unrated", "remastered", "hdr", "hdr10", "sdr", "proper", "repack", "complete", "bluray", "webrip", "hevc", "h264", "h265", "x264", "x265", "av1":
		return false
	}
	return true
}
