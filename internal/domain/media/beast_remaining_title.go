package media

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Boundary evidence: fixed MIT TheBeastLT/parse-torrent-title
// 2fb0c592143b3a86ba06e06b18068077334526c8 test/title.js:60-66,220-221
// and test/main.js:301-310. Publisher spellings are independently asserted
// by original title inputs, not learned from the frozen Go expectations.
// This view never supplies year, coverage, confidence or quality evidence.
var beastRemainingRussianAlias = regexp.MustCompile(`^([\p{Cyrillic} ,.!?«»—-]+) \(([A-Za-z][A-Za-z0-9 ,.!?'’:/-]*)\) ((?:19|20)[0-9]{2}) (?:BDRip|BRRip|BluRay) (?:720p|1080p|2160p)(?:\.(?:mkv|mp4))?$`)
var beastRemainingBracketMovie = regexp.MustCompile(`^\[SweetSub\]\[([A-Za-z][A-Za-z0-9 ,.!?'’:/-]*)\]\[Movie\]\[BDRip\]\[(?:720|1080|2160)P\]\[AVC 8bit\]\[简体内嵌\]$`)
var beastRemainingJapaneseAlias = regexp.MustCompile(`^\[Seed-Raws\] ([\p{Han}\p{Hiragana}\p{Katakana}・ー ]+) ([A-Za-z][A-Za-z0-9 ,.!?'’:/-]*) \(BD (?:1280x720|1920x1080) AVC AACx[1-9] \[[0-9]\.[01](?:\+[0-9]\.[01]){1,4}\]\)\.mp4$`)

// Preserve the complete compound prefix. It is not permission to strip other
// technical brackets or certify arbitrary relay tags.
var beastRemainingCompound = regexp.MustCompile(`^\[BluRay Rip (?:720p|1080p) ITA AC3 - ENG AC3 SUB\] ([A-Za-z][A-Za-z0-9 ,.!?'’:/-]*)\[((?:19|20)[0-9]{2})\]-([A-Za-z][A-Za-z0-9]{1,23})\[ultimafrontiera\]$`)
var beastRemainingCoverage = regexp.MustCompile(`(?i)(?:\bS[0-9]+|\bE[0-9]+|\b[0-9]+x[0-9]+|\b(?:season|episode|complete|pack|OVA|special|sample|trailer|part|disc|vol|by|directed|starring|subtitles?)\b)`)

func beastRemainingSafeTitle(s string) bool {
	if strings.TrimSpace(s) != s || s == "" || beastRemainingCoverage.MatchString(s) {
		return false
	}
	// A title slash is retained only between nonempty Latin title segments;
	// directory-looking, numeric episode-list and backslash forms decline.
	if strings.ContainsAny(s, `\`) || strings.Contains(s, "//") || strings.Contains(s, "../") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		hasLetter := false
		for _, r := range part {
			if unicode.IsLetter(r) {
				hasLetter = true
			}
		}
		if !hasLetter {
			return false
		}
	}
	return true
}

func beastRemainingTitleView(raw string, workYear int) (string, bool) {
	if strings.ContainsAny(raw, "\r\n\\") {
		return "", false
	}
	if m := beastRemainingRussianAlias.FindStringSubmatch(raw); m != nil {
		year, _ := strconv.Atoi(m[3])
		if year == workYear && beastRemainingSafeTitle(m[2]) {
			return m[2], true
		}
	}
	if workYear == 0 {
		for _, re := range []*regexp.Regexp{beastRemainingBracketMovie, beastRemainingJapaneseAlias} {
			if m := re.FindStringSubmatch(raw); m != nil && beastRemainingSafeTitle(m[len(m)-1]) {
				title := m[len(m)-1]
				// A competing work year remains raw input evidence, never an alias hint.
				if !yearRegex.MatchString(title) {
					return title, true
				}
			}
		}
	}
	if m := beastRemainingCompound.FindStringSubmatch(raw); m != nil {
		year, _ := strconv.Atoi(m[2])
		if workYear == year && !yearRegex.MatchString(m[1]) && beastRemainingSafeTitle(m[1]) {
			return m[1], true
		}
	}
	return "", false
}

// Only the independent full-object main.js assertion certifies this relay.
// Keep an unknown suffix verbatim. Do not apply this to arbitrary filenames.
func beastRemainingRelayGroup(raw string, workYear int) (string, bool) {
	m := beastRemainingCompound.FindStringSubmatch(raw)
	if m == nil {
		return "", false
	}
	year, _ := strconv.Atoi(m[2])
	if year != workYear || !beastRemainingSafeTitle(m[1]) || yearRegex.MatchString(m[1]) {
		return "", false
	}
	return m[3], true
}
