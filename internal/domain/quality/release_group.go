package quality

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Raw title evidence is used only to deny a matching suffix. It never fills in
// a missing historical Group, grants rank or certifies a release identity.
func rejectedRawReleaseGroup(title, rejected string) bool {
	base := filepath.Base(title)
	switch strings.ToLower(filepath.Ext(base)) {
	case ".mkv", ".mp4", ".avi", ".m4v", ".mov", ".wmv", ".ts", ".m2ts", ".iso":
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	base = WithoutReleaseIdentityTags(base)
	if at := ReleaseGroupDelimiter(base); at >= 0 {
		return RejectedReleaseGroup(base[at+1:], rejected)
	}
	return false
}

// RejectedReleaseGroup compares negative policy evidence only. A trailing
// bracket tag must not disguise a rejected group, including historical raw
// group observations. This does not certify a group or mutate stored facts.
func RejectedReleaseGroup(observed, rejected string) bool {
	rejected = strings.TrimSpace(rejected)
	if rejected == "" {
		return false
	}
	observed = strings.TrimSpace(observed)
	if strings.EqualFold(observed, rejected) {
		return true
	}
	// A bare filename container after a literal group is negative evidence only.
	// Do not certify a group, fill missing historical fields or strip arbitrary words.
	parts := strings.Fields(observed)
	if len(parts) == 2 && strings.EqualFold(parts[0], rejected) {
		switch strings.ToLower(parts[1]) {
		case "mkv", "mp4", "avi", "m4v", "mov", "wmv", "ts", "m2ts", "iso":
			return true
		}
	}
	if at := strings.IndexByte(observed, '['); at > 0 && strings.HasSuffix(observed, "]") {
		return RejectedReleaseGroup(strings.TrimSpace(observed[:at]), rejected)
	}
	return false
}

// ReleaseGroupDelimiter locates a possible group suffix, not trusted identity.
// A dash inside a complete technical marker is never a group delimiter.
func ReleaseGroupDelimiter(name string) int {
	at := lastDashOutsidePairedDecorations(name)
	if at < 0 || at == len(name)-1 {
		return -1
	}
	tail := strings.TrimSpace(name[at+1:])
	if bracket := strings.IndexByte(tail, '['); bracket > 0 && strings.HasSuffix(tail, "]") {
		tail = strings.TrimSpace(tail[:bracket])
	}
	for _, marker := range []struct{ head, tail string }{{"web", "dl"}, {"blu", "ray"}, {"dts", "hd"}} {
		start := at - len(marker.head)
		if start < 0 || !strings.EqualFold(name[start:at], marker.head) || !strings.EqualFold(tail, marker.tail) {
			continue
		}
		if start == 0 {
			return -1
		}
		before, _ := utf8.DecodeLastRuneInString(name[:start])
		if !unicode.IsLetter(before) && !unicode.IsDigit(before) {
			return -1
		}
	}
	return at
}

// Paired decorations do not supply a group delimiter. Preserve their bytes in
// the suffix: unknown tags remain raw evidence and are never certified relays.
// Mismatched or incomplete decorations are not repaired or stripped.
func lastDashOutsidePairedDecorations(name string) int {
	var opens, dashes []int
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '[', '(':
			opens = append(opens, i)
		case ']', ')':
			if len(opens) == 0 {
				continue
			}
			start := opens[len(opens)-1]
			if (name[start] == '[' && name[i] != ']') || (name[start] == '(' && name[i] != ')') {
				opens = nil
				continue
			}
			opens = opens[:len(opens)-1]
			for len(dashes) > 0 && dashes[len(dashes)-1] > start {
				dashes = dashes[:len(dashes)-1]
			}
		case '-':
			dashes = append(dashes, i)
		}
	}
	if len(dashes) == 0 {
		return -1
	}
	return dashes[len(dashes)-1]
}
