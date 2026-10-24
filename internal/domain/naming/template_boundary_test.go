package naming_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
)

func TestNamingTemplateSinglePassAndVariableIsolation(t *testing.T) {
	vars := map[string]string{"title": "{year}", "year": "1999", "unused": "../../private"}
	before, _ := json.Marshal(vars)
	for range 100 {
		got, err := naming.FormatNamingTemplate("{title}/{year}.mkv", vars)
		if err != nil || got != "{year}/1999.mkv" {
			t.Fatal("metadata was recursively interpreted", got, err)
		}
	}
	after, _ := json.Marshal(vars)
	if string(before) != string(after) {
		t.Fatal("input map changed")
	}
}

func TestNamingTemplateRejectsInvalidSyntax(t *testing.T) {
	for _, value := range []string{"", " ", "{unknown}.mkv", "{title", "title}", "{}", "{{title}}", "{season:02d}", "{Title}", "{ title}", "/absolute/file.mkv", "C:/drive/file.mkv", "\\\\server\\share\\file.mkv", "a//file.mkv", "./file.mkv", "a/../file.mkv", "../file.mkv", "a/.hidden/file.mkv", "a/.pulsemedia_root", "a/file.", "CON.mkv", "a/NUL", "Lpt1.txt", "a/name?.mkv"} {
		t.Run(value, func(t *testing.T) {
			if !errors.Is(naming.ValidateNamingTemplate(value), naming.ErrInvalidNamingTemplate) {
				t.Fatal("unsafe or ambiguous syntax passed")
			}
			if _, err := naming.FormatNamingTemplate(value, map[string]string{"title": "Title", "season": "01"}); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
				t.Fatal("unsafe syntax rendered")
			}
		})
	}
}

func TestNamingTemplateRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{"", "../escape", "a/b", "a\\b", "a:b", "bad\x00name", "bad\nname", "bad\rname", "bad\u202ename", " leading", "trailing ", string([]byte{0xff})} {
		if _, err := naming.FormatNamingTemplate("{title}.mkv", map[string]string{"title": value}); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
			t.Fatalf("unsafe value accepted (bytes=%d)", len(value))
		}
	}
	if _, err := naming.FormatNamingTemplate("{title} ({year}).mkv", map[string]string{"title": "Unknown year"}); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
		t.Fatal("missing year fabricated")
	}
}

func TestNamingTemplatePathAndByteBounds(t *testing.T) {
	if err := naming.ValidateNamingTemplate(strings.Repeat("a", naming.MaxNamingTemplateBytes+1)); err == nil {
		t.Fatal("oversize template accepted")
	}
	for _, tc := range []struct {
		template, title string
		ok              bool
	}{
		{"{title}.mkv", strings.Repeat("a", 251), true},
		{"{title}.mkv", strings.Repeat("a", 252), false},
		{"{title}.mkv", strings.Repeat("中", 83), true},
		{"{title}.mkv", strings.Repeat("中", 84), false},
		{strings.Repeat("a/", 15) + "{title}", "a", true},
		{strings.Repeat("a/", 16) + "{title}", "a", false},
		{strings.Repeat("{title}", 30), strings.Repeat("a", 255), false},
	} {
		_, err := naming.FormatNamingTemplate(tc.template, map[string]string{"title": tc.title})
		if (err == nil) != tc.ok {
			t.Fatal("path byte/depth boundary mismatch", err)
		}
	}
}

func TestNamingTemplateTVPreviewAndMissingFacts(t *testing.T) {
	template := "{title} ({year})/Season {season}/{title} - {episode_key} [{resolution} {source}].{extension}"
	vars := map[string]string{"title": "稀疏剧集", "year": "2020", "season": "01", "episode": "07", "episode_key": "S01E07", "resolution": "1080p", "source": "WEB-DL", "extension": "mkv"}
	if err := naming.ValidateNamingTemplate(template); err != nil {
		t.Fatal(err)
	}
	got, err := naming.FormatNamingTemplate(template, vars)
	if err != nil || got != "稀疏剧集 (2020)/Season 01/稀疏剧集 - S01E07 [1080p WEB-DL].mkv" {
		t.Fatal("TV preview mismatch", got, err)
	}
	delete(vars, "episode_key")
	if _, err = naming.FormatNamingTemplate(template, vars); err == nil {
		t.Fatal("unknown episode identity fabricated")
	}
}

func TestNamingTemplateErrorsDoNotEchoValues(t *testing.T) {
	const secret = "private-template-value"
	for _, tc := range []struct {
		template string
		vars     map[string]string
	}{
		{"{" + secret + "}.mkv", nil},
		{"{title}.mkv", map[string]string{"title": secret + "/../../escape"}},
	} {
		_, err := naming.FormatNamingTemplate(tc.template, tc.vars)
		if !errors.Is(err, naming.ErrInvalidNamingTemplate) || strings.Contains(err.Error(), secret) {
			t.Fatal("error exposed template or metadata")
		}
	}
}
