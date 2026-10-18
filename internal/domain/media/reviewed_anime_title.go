package media

import (
	"path/filepath"
	"regexp"
	"strings"
)

// These publisher spellings and complete tail layouts are literal evidence from
// MIT lowlighter/rakun@9cb4960da05b79c4bcea3eafc1b5f2ca6c816b56,
// tests/cases/all.js records 2, 11, 18, 19, 38, 39, 41, 48, 57, 59, 76.
// They describe title boundaries only, not quality, identity or coverage.
var reviewedAnimeTitlePublisher = regexp.MustCompile(`^\[(?:Ohys-Raws|Teke|Cleo|pcela-anime|Cervoz|JpU - No oficial|Reaktor|UQW|matheousse|SOFCJ-Raws|Hi10)\][ \t_]+`)

// Keep complete observed layouts together: recognizing one technical word must
// not erase an unknown bracket, trailing title continuation or arbitrary token.
var reviewedAnimeTitleTail = regexp.MustCompile(`(?:[ \t]+\(BD 1920x1080 x264 FLACx2\)|_BDRip_AAC_1080MP4|[ \t]+\[Dual Audio 10bit BD720p\]\[HEVC-x265\]|[ \t]+\[BRrip 1920x1080 x264 AAC\] \[SUB PL\+EN\]|[ \t]+\[1080p\]\[MULTI\]\[VF - VOSTFR\]\[10Bits\]|[ \t]+\[1080p\]\[BD\]\[x265\]\[10-bit\]\[Dual-Audio\]|[ \t]+\[BD 1080p AVC-YUV420P10 FLAC\]|[ \t]+MULTi VF/VOSTFR \(BD 1080p AAC\)|[ \t]+\(BDRip 1920x1080 x264 10bit VFR FLAC\)|[ \t]+\[DVD 576p\])$`)

// reviewedAnimeTitleView supplies a lexical title for the yearless movie-title
// fallback, after all original-input coverage holds. Callers must keep the raw
// input for quality/year/coverage checks; this view never authorizes work.
// On an unreviewed layout it declines the entire repair.
func reviewedAnimeTitleView(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if strings.ContainsAny(name, "\r\n") {
		return "", false
	}
	// Do not use filepath.Base: the observed language tail VF/VOSTFR contains
	// a slash. Actual path prefixes are not certified publishers.
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".avi", ".m4v", ".mov", ".wmv", ".ts", ".m2ts", ".iso":
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	prefix := reviewedAnimeTitlePublisher.FindStringIndex(name)
	if prefix == nil {
		return "", false
	}
	body := name[prefix[1]:]
	if tail := reviewedAnimeTitleTail.FindStringIndex(body); tail != nil {
		body = body[:tail[0]]
	} else if !strings.HasPrefix(name, "[JpU - No oficial]") {
		// Only this source-confirmed publisher-only layout lacks a tail.
		return "", false
	}
	title := strings.TrimSpace(body)
	if title == "" || strings.ContainsAny(title, "[]{}\\/") {
		return "", false
	}
	return title, true
}
