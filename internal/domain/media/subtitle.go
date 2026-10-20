package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	MaxChineseSubtitleBytes = 2 * 1024 * 1024
	MinShooterVideoBytes    = 16384
)

var ErrInvalidSubtitle = errors.New("invalid Chinese subtitle")

var (
	srtSimpleMarkup = regexp.MustCompile(`(?i)</?(?:b|i|u)>|</?font(?:[ \t]+(?:color|face|size)[ \t]*=[ \t]*(?:"[^"<>\n]*"|'[^'<>\n]*'|[a-z0-9#.+-]+))*[ \t]*>`)
	srtOtherMarkup  = regexp.MustCompile(`(?i)<[!/a-z]`)
)

// NormalizeChineseSRT validates bounded SRT content and emits sequential cues.
// Han presence is a language hint, not proof of translation or simplified Chinese.
func NormalizeChineseSRT(input []byte) ([]byte, error) {
	if len(input) == 0 || len(input) > MaxChineseSubtitleBytes {
		return nil, ErrInvalidSubtitle
	}
	text, ok := decodeChineseSRT(input)
	if !ok || len(text) > MaxChineseSubtitleBytes {
		return nil, ErrInvalidSubtitle
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for _, r := range text {
		if r == utf8.RuneError || r == '\ufeff' || (unicode.IsControl(r) && r != '\n' && r != '\t') {
			return nil, ErrInvalidSubtitle
		}
	}
	lines := strings.Split(text, "\n")
	var out strings.Builder
	count, hasHan := 0, false
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		if srtIndex(lines[i]) {
			i++
		}
		if i >= len(lines) || !srtTiming(lines[i]) {
			return nil, ErrInvalidSubtitle
		}
		timing := lines[i]
		i++
		start := i
		cueText := false
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			// A second timing line without a cue separator is ambiguous.
			if srtTiming(lines[i]) {
				return nil, ErrInvalidSubtitle
			}
			plain := srtSimpleMarkup.ReplaceAllString(lines[i], "")
			if srtOtherMarkup.MatchString(plain) {
				return nil, ErrInvalidSubtitle
			}
			for _, r := range plain {
				cueText = cueText || !unicode.IsSpace(r)
				hasHan = hasHan || unicode.Is(unicode.Han, r)
			}
			i++
		}
		if !cueText || count == 2147483647 {
			return nil, ErrInvalidSubtitle
		}
		count++
		cue := strconv.Itoa(count) + "\n" + timing + "\n" + strings.Join(lines[start:i], "\n") + "\n\n"
		if len(cue) > MaxChineseSubtitleBytes-out.Len() {
			return nil, ErrInvalidSubtitle
		}
		out.WriteString(cue)
	}
	if count == 0 || !hasHan {
		return nil, ErrInvalidSubtitle
	}
	return []byte(out.String()), nil
}

func decodeChineseSRT(input []byte) (string, bool) {
	if bytes.HasPrefix(input, []byte{0xff, 0xfe}) || bytes.HasPrefix(input, []byte{0xfe, 0xff}) {
		var order binary.ByteOrder = binary.BigEndian
		if input[0] == 0xff {
			order = binary.LittleEndian
		}
		input = input[2:]
		if len(input)%2 != 0 {
			return "", false
		}
		var out strings.Builder
		for i := 0; i < len(input); i += 2 {
			u := order.Uint16(input[i:])
			r := rune(u)
			if u >= 0xd800 && u <= 0xdbff {
				if i+4 > len(input) {
					return "", false
				}
				v := order.Uint16(input[i+2:])
				if v < 0xdc00 || v > 0xdfff {
					return "", false
				}
				r = utf16.DecodeRune(rune(u), rune(v))
				i += 2
			} else if u >= 0xdc00 && u <= 0xdfff {
				return "", false
			}
			out.WriteRune(r)
			if out.Len() > MaxChineseSubtitleBytes {
				return "", false
			}
		}
		return out.String(), true
	}
	if bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) {
		input = input[3:]
		return string(input), utf8.Valid(input)
	}
	if utf8.Valid(input) {
		return string(input), true
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(input)
	if err != nil || bytes.Contains(decoded, []byte("\ufffd")) {
		return "", false
	}
	// x/text decoders can substitute invalid sequences; require a lossless decode.
	roundTrip, err := simplifiedchinese.GB18030.NewEncoder().Bytes(decoded)
	return string(decoded), err == nil && bytes.Equal(input, roundTrip)
}

func srtIndex(line string) bool {
	if len(line) == 0 || len(line) > 10 {
		return false
	}
	for _, c := range line {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(line, 10, 31)
	return err == nil && n > 0
}

func srtTiming(line string) bool {
	if len(line) != 29 || line[12:17] != " --> " {
		return false
	}
	start, ok := srtMilliseconds(line[:12])
	end, endOK := srtMilliseconds(line[17:])
	return ok && endOK && end >= start
}

func srtMilliseconds(s string) (int64, bool) {
	if len(s) != 12 || s[2] != ':' || s[5] != ':' || s[8] != ',' {
		return 0, false
	}
	var n [4]int64
	part := 0
	for i, c := range s {
		if i == 2 || i == 5 || i == 8 {
			part++
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		n[part] = n[part]*10 + int64(c-'0')
	}
	return ((n[0]*60+n[1])*60+n[2])*1000 + n[3], n[1] < 60 && n[2] < 60
}

// ChineseSubtitleName derives a canonical root-relative path. It grants no
// filesystem or symlink authority; callers must resolve beneath their root.
func ChineseSubtitleName(rootRelativeVideo string) (string, error) {
	if rootRelativeVideo == "" || len(rootRelativeVideo) > 4095 || !utf8.ValidString(rootRelativeVideo) || path.IsAbs(rootRelativeVideo) || path.Clean(rootRelativeVideo) != rootRelativeVideo {
		return "", ErrInvalidSubtitle
	}
	for _, part := range strings.Split(rootRelativeVideo, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return "", ErrInvalidSubtitle
		}
		for _, r := range part {
			if unicode.IsControl(r) || r == utf8.RuneError || strings.ContainsRune(`\:*?"<>|%`, r) {
				return "", ErrInvalidSubtitle
			}
		}
	}
	ext := path.Ext(rootRelativeVideo)
	switch strings.ToLower(ext) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".ts", ".m2ts", ".webm", ".wmv", ".mpg", ".mpeg":
	default:
		return "", ErrInvalidSubtitle
	}
	base := strings.TrimSuffix(path.Base(rootRelativeVideo), ext)
	name := strings.TrimSuffix(rootRelativeVideo, ext) + ".zh-cn.default.srt"
	if base == "" || strings.Trim(base, ". ") == "" || len(path.Base(name)) > 255 || len(name) > 4095 {
		return "", ErrInvalidSubtitle
	}
	return name, nil
}

// ValidShooterHash checks syntax and minimum size, not file identity or authority.
func ValidShooterHash(hash string, sizeBytes int64) bool {
	if sizeBytes < MinShooterVideoBytes || len(hash) != 4*32+3 {
		return false
	}
	for i, c := range hash {
		if i == 32 || i == 65 || i == 98 {
			if c != ';' {
				return false
			}
		} else if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
