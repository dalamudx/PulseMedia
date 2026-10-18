package media

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// releaseLexicalTitleView is a display-only correction, called after the legacy
// movie title extraction. Never pass its output back to year, quality, identity,
// coverage or confidence parsing. Evidence: MIT TheBeastLT/parse-torrent-title
// 2fb0c592143b3a86ba06e06b18068077334526c8, test/title.js lines 55-56,
// 105-121 and 310-316. There are no work names or inferred translations here.
var lexicalHi10Tail = regexp.MustCompile(`^(.+?)[ \t]+★Hi10★[ \t]+\[(?:480|720|1080|2160)p\]$`)
var lexicalNumericSlash = regexp.MustCompile(`^((?:[\p{Latin}][\p{Latin} '\-]*[ \t]+)?[0-9]{1,2}/[0-9]{1,2})[ \t]+(?:\((?:19|20)[0-9]{2}\)|(?:19|20)[0-9]{2})[ \t]+(?i:1080p|720p|2160p|480p|WEB-DL)$`)

// A complete year/country/genre/rip block establishes a metadata boundary.
// Unknown brackets, numbers and unrecognized suffixes fail the entire view.
var lexicalAliasMetadata = regexp.MustCompile(`^\[(?:19|20)[0-9]{2},[\p{Cyrillic}\p{Latin} ,().'\-]+,[ \t]*(?i:BDRip|DVDRip)\](?:[ \t]+(?:MVO[ \t]+\([\p{Cyrillic} .]+\)|[1-9][ \t]+VO))?$`)
var lexicalNameBlock = regexp.MustCompile(`^[\p{Cyrillic}\p{Latin} /().'\-]+$`)

func releaseLexicalTitleView(raw string, workYear int) (string, bool) {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\\r\n") {
		return "", false
	}
	name := raw
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".avi", ".m4v", ".mov", ".wmv", ".ts", ".m2ts", ".iso":
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	// The full original evidence must agree with the already computed work year.
	// This never chooses between years or rescues a year lost in a path segment.
	if year, ambiguous := recognizeYear(name); ambiguous || year != workYear {
		return "", false
	}
	if workYear == 0 {
		if match := lexicalHi10Tail.FindStringSubmatch(name); match != nil && lexicalPlainTitle(match[1]) {
			return match[1], true
		}
		return "", false
	}
	if match := lexicalNumericSlash.FindStringSubmatch(name); match != nil {
		return match[1], true
	}
	at := strings.Index(name, " [")
	if at < 0 || !lexicalAliasMetadata.MatchString(name[at+1:]) {
		return "", false
	}
	head := strings.TrimSpace(name[:at])
	// Require exactly one balanced trailing parenthesized metadata block. Scan
	// nesting instead of splitting on the last slash (which may be a director).
	open, depth := -1, 0
	for i, r := range head {
		switch r {
		case '(':
			if depth == 0 {
				if open >= 0 {
					return "", false
				}
				open = i
			}
			depth++
		case ')':
			depth--
			if depth < 0 || (depth == 0 && i != len(head)-1) {
				return "", false
			}
		}
	}
	if open <= 0 || depth != 0 || !strings.HasSuffix(head, ")") || !lexicalNameBlock.MatchString(head[open+1:len(head)-1]) {
		return "", false
	}
	aliases := strings.Split(strings.TrimSpace(head[:open]), " / ")
	if len(aliases) < 2 || !lexicalScriptTitle(aliases[0], unicode.Cyrillic) {
		return "", false
	}
	for _, alias := range aliases[1:] {
		if !lexicalScriptTitle(alias, unicode.Latin) {
			return "", false
		}
	}
	return strings.Join(aliases[1:], " / "), true
}

func lexicalPlainTitle(s string) bool {
	return strings.TrimSpace(s) == s && s != "" && !strings.ContainsAny(s, "[]{}()/\\\r\n")
}

func lexicalScriptTitle(s string, script *unicode.RangeTable) bool {
	if !lexicalPlainTitle(s) {
		return false
	}
	letter := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			if !unicode.Is(script, r) {
				return false
			}
			letter = true
		} else if !unicode.IsSpace(r) && !unicode.IsDigit(r) && !strings.ContainsRune("'’.,:!?-", r) {
			return false
		}
	}
	return letter
}
