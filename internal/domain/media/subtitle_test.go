package media_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const subtitleTiming = "00:00:01,000 --> 00:00:03,500"

func TestNormalizeChineseSRTEncodingsAndCues(t *testing.T) {
	input := "42\r\n" + subtitleTiming + "\r\n<i>你好</i>\r\n123\r\n时间 00:00:02,000\r\n\r\n00:00:04,000 --> 00:00:05,000\r\n<font color=\"#ffffff\">世界</font>\r\nEnglish too\r\n"
	want := "1\n" + subtitleTiming + "\n<i>你好</i>\n123\n时间 00:00:02,000\n\n2\n00:00:04,000 --> 00:00:05,000\n<font color=\"#ffffff\">世界</font>\nEnglish too\n\n"
	for name, input := range map[string][]byte{
		"utf8 multiline": []byte(input),
		"utf8 BOM":       append([]byte{0xef, 0xbb, 0xbf}, []byte(input)...),
		"CR only":        []byte(strings.ReplaceAll(input, "\r\n", "\r")),
	} {
		t.Run(name, func(t *testing.T) {
			original := bytes.Clone(input)
			got, err := media.NormalizeChineseSRT(input)
			if err != nil || string(got) != want || !utf8.Valid(got) {
				t.Fatalf("got %q, %v; want %q", got, err, want)
			}
			if !bytes.Equal(input, original) {
				t.Fatal("mutated input")
			}
			again, err := media.NormalizeChineseSRT(got)
			if err != nil || !bytes.Equal(again, got) {
				t.Fatalf("normalization is not idempotent: %q, %v", again, err)
			}
		})
	}
	// Fixed GB2312 bytes for 中文, independent of the decoder under test.
	gb2312 := append([]byte(subtitleTiming+"\n"), 0xd6, 0xd0, 0xce, 0xc4)
	got, err := media.NormalizeChineseSRT(gb2312)
	if err != nil || string(got) != "1\n"+subtitleTiming+"\n中文\n\n" {
		t.Fatalf("GB2312: %q, %v", got, err)
	}
	text := subtitleTiming + "\n中文𠀀😀"
	gb18030, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	encodings := map[string][]byte{"GB18030 four-byte": gb18030}
	for _, little := range []bool{true, false} {
		var order binary.ByteOrder = binary.BigEndian
		bom, name := []byte{0xfe, 0xff}, "UTF16BE"
		if little {
			order, bom, name = binary.LittleEndian, []byte{0xff, 0xfe}, "UTF16LE"
		}
		encoded := bytes.Clone(bom)
		for _, u := range utf16.Encode([]rune(text)) {
			var pair [2]byte
			order.PutUint16(pair[:], u)
			encoded = append(encoded, pair[:]...)
		}
		encodings[name] = encoded
	}
	for name, input := range encodings {
		t.Run(name, func(t *testing.T) {
			got, err := media.NormalizeChineseSRT(input)
			if err != nil || string(got) != "1\n"+text+"\n\n" {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestNormalizeChineseSRTRejectsInvalidContent(t *testing.T) {
	cue := func(s string) []byte { return []byte(subtitleTiming + "\n" + s) }
	cases := map[string][]byte{
		"empty":                     nil,
		"English only":              cue("Hello world"),
		"not subtitle":              []byte("中文字幕下载失败"),
		"HTML error":                []byte("<!DOCTYPE html><html><body>错误</body></html>"),
		"HTML cue":                  cue("<html><body>错误</body></html>"),
		"HTML script":               cue("中文<script>alert(1)</script>"),
		"Han only in attribute":     cue(`<font face="中文">English</font>`),
		"markup only":               cue("<i></i>"),
		"NUL":                       cue("中\x00文"),
		"control":                   cue("中\x1b文"),
		"DEL":                       cue("中\x7f文"),
		"replacement":               cue("中\ufffd文"),
		"interior BOM":              cue("中\ufeff文"),
		"missing text":              []byte("1\n" + subtitleTiming + "\n\n"),
		"whitespace text":           cue(" \t"),
		"bad later cue":             []byte(subtitleTiming + "\n中文\n\n2\nwrong\n正文"),
		"missing separator":         []byte(subtitleTiming + "\n中文\n2\n" + subtitleTiming + "\n正文"),
		"ASS":                       []byte("[Script Info]\n[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,中文"),
		"index overflow":            []byte("2147483648\n" + subtitleTiming + "\n中文"),
		"huge index":                []byte(strings.Repeat("9", 1000) + "\n" + subtitleTiming + "\n中文"),
		"zero index":                []byte("0\n" + subtitleTiming + "\n中文"),
		"negative index":            []byte("-1\n" + subtitleTiming + "\n中文"),
		"truncated GB":              append(cue("中文"), 0x81),
		"bad UTF8 BOM":              append([]byte{0xef, 0xbb, 0xbf}, append(cue("中文"), 0xff)...),
		"odd UTF16":                 {0xff, 0xfe, 0x2d},
		"UTF16 lone high surrogate": {0xff, 0xfe, 0x00, 0xd8},
		"UTF16 lone low surrogate":  {0xfe, 0xff, 0xdc, 0x00},
		"UTF16 invalid pair":        {0xff, 0xfe, 0x00, 0xd8, 0x2d, 0x4e},
	}
	for _, timing := range []string{
		"00:00:03,501 --> 00:00:03,500", "00:60:01,000 --> 00:61:02,000",
		"00:00:60,000 --> 00:01:01,000", "100:00:00,000 --> 101:00:00,000",
		"999999999999999999999:00:00,000 --> 00:00:01,000",
		"00:00:01.000 --> 00:00:03.500", "0:00:01,000 --> 00:00:03,500",
		"00:00:01,000 -> 00:00:03,500", "00:00:01,000 --> 00:00:03,500 X1:10",
		"00:00:01,000 --> 00:00:03,5000",
	} {
		cases["timing "+timing] = []byte(timing + "\n中文")
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := media.NormalizeChineseSRT(input)
			if !errors.Is(err, media.ErrInvalidSubtitle) || got != nil {
				t.Fatalf("accepted invalid subtitle: len=%d, err=%v", len(got), err)
			}
		})
	}
}

func TestNormalizeChineseSRTLimits(t *testing.T) {
	if media.MaxChineseSubtitleBytes != 2*1024*1024 || media.MinShooterVideoBytes != 16384 {
		t.Fatal("unexpected public limits")
	}
	prefix := "1\n" + subtitleTiming + "\n中"
	atLimit := prefix + strings.Repeat("a", media.MaxChineseSubtitleBytes-len(prefix)-2) + "\n\n"
	got, err := media.NormalizeChineseSRT([]byte(atLimit))
	if err != nil || string(got) != atLimit {
		t.Fatalf("exact limit rejected: len=%d, err=%v", len(got), err)
	}
	for name, input := range map[string][]byte{
		"input limit":       []byte(atLimit + "a"),
		"output expansion":  []byte(strings.TrimPrefix(atLimit, "1\n") + "a"),
		"decoded expansion": bytes.Repeat([]byte{0xd6, 0xd0}, media.MaxChineseSubtitleBytes/2),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := media.NormalizeChineseSRT(input); !errors.Is(err, media.ErrInvalidSubtitle) || got != nil {
				t.Fatalf("accepted oversized subtitle: len=%d err=%v", len(got), err)
			}
		})
	}
}

func TestChineseSubtitleName(t *testing.T) {
	for _, ext := range []string{"mkv", "mp4", "avi", "mov", "m4v", "ts", "m2ts", "webm", "wmv", "mpg", "mpeg"} {
		input := "电影/多点.title.2026." + strings.ToUpper(ext)
		want := "电影/多点.title.2026.zh-cn.default.srt"
		if got, err := media.ChineseSubtitleName(input); err != nil || got != want {
			t.Errorf("%q: got %q, %v", input, got, err)
		}
	}
	maxBase := strings.Repeat("a", 255-len(".zh-cn.default.srt"))
	for _, input := range []string{"movie.mkv", ".hidden.mkv", "目录/电影 名称.mp4", maxBase + ".mkv"} {
		want := input[:strings.LastIndex(input, ".")] + ".zh-cn.default.srt"
		if got, err := media.ChineseSubtitleName(input); err != nil || got != want {
			t.Errorf("valid %q: %q, %v", input, got, err)
		}
	}
	for _, input := range []string{
		"", ".", "..", "movie", ".mkv", "..mkv", "...mkv", "movie.srt", "movie.mkv/",
		"/movie.mkv", "//host/movie.mkv", "../movie.mkv", "a/../../movie.mkv",
		"a/../movie.mkv", "./movie.mkv", "a/./movie.mkv", "a//movie.mkv",
		`a\movie.mkv`, `C:\movie.mkv`, "C:/movie.mkv", "https://host/movie.mkv",
		"%2e%2e/movie.mkv", "a%2fb.mkv", "movie.mkv?x", "movie\x00.mkv", "movie\n.mkv",
		" movie.mkv", "movie.mkv ", "a /movie.mkv", "a./movie.mkv", "a*/movie.mkv",
		string([]byte{0xff}) + ".mkv", maxBase + "a.mkv", strings.Repeat("中", 80) + ".mkv",
		strings.Repeat("a", 256) + "/movie.mkv", strings.Repeat("aaaa/", 820) + "movie.mkv",
	} {
		if got, err := media.ChineseSubtitleName(input); !errors.Is(err, media.ErrInvalidSubtitle) || got != "" {
			t.Errorf("accepted invalid path len=%d: %q, %v", len(input), got, err)
		}
	}
}

func TestValidShooterHash(t *testing.T) {
	group := "0123456789abcdef0123456789abcdef"
	valid := strings.Repeat(group+";", 3) + group
	for _, size := range []int64{16384, 16385, 1<<63 - 1} {
		if !media.ValidShooterHash(valid, size) {
			t.Errorf("rejected valid hash at size %d", size)
		}
	}
	for _, size := range []int64{-1, 0, 16383} {
		if media.ValidShooterHash(valid, size) {
			t.Errorf("accepted undersized file %d", size)
		}
	}
	for _, hash := range []string{"", group, strings.ToUpper(valid), valid + ";", valid[:len(valid)-1], " " + valid,
		strings.ReplaceAll(valid, ";", ":"), strings.Replace(valid, "a", "g", 1), strings.Replace(valid, ";", "", 1),
		strings.Replace(valid, ";", "\n", 1), strings.Replace(valid, "0", "中", 1)} {
		if media.ValidShooterHash(hash, 16384) {
			t.Errorf("accepted invalid hash %q", hash)
		}
	}
}
