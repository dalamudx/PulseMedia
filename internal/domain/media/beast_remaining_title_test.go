package media

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// Synthetic boundary controls are not independent gold. The shared strict
// source + seven-quality contract test remains the authority for frozen rows.
func TestBeastRemainingFrozenTitleViews(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/releases/review-inputs/pending-frozen-contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	var pending struct {
		Entries []struct {
			Source string `json:"source"`
			Record struct {
				ID       string `json:"id"`
				Input    string `json:"input"`
				Source   string `json:"source"`
				Expected struct {
					Title string `json:"title"`
					Year  int    `json:"year"`
				} `json:"independent_expected"`
			} `json:"record"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &pending); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range pending.Entries {
		switch row.Record.ID {
		case "beast-next-of-mice", "beast-next-penguin-highway", "beast-next-mutafukaz", "beast-next-hostel":
			count++
			t.Run(row.Record.ID, func(t *testing.T) {
				if row.Source != "beast-full-original" || row.Record.Source != row.Source {
					t.Fatal("source changed")
				}
				got, ok := beastRemainingTitleView(row.Record.Input, row.Record.Expected.Year)
				if !ok || got != row.Record.Expected.Title {
					t.Fatalf("view %q,%v want unchanged %q", got, ok, row.Record.Expected.Title)
				}
			})
		}
	}
	if count != 4 {
		t.Fatalf("frozen rows: %d", count)
	}
}

func TestBeastRemainingNarrowLayouts(t *testing.T) {
	russian := "Локальное имя (English Alias) 1992 BDRip 1080p.mkv"
	bracket := "[SweetSub][English Alias / Alias Two][Movie][BDRip][1080P][AVC 8bit][简体内嵌]"
	japanese := "[Seed-Raws] 劇場版 日本語 English Alias (BD 1280x720 AVC AACx4 [5.1+2.0+2.0+2.0]).mp4"
	compound := "[BluRay Rip 720p ITA AC3 - ENG AC3 SUB] English Alias[2005]-TEAM[ultimafrontiera]"
	for i, row := range []struct {
		raw   string
		year  int
		title string
	}{
		{russian, 1992, "English Alias"}, {bracket, 0, "English Alias / Alias Two"},
		{japanese, 0, "English Alias"}, {compound, 2005, "English Alias"},
		{strings.Replace(russian, "English Alias", "Web Cam", 1), 1992, "Web Cam"},
	} {
		t.Run("positive-"+string(rune('a'+i)), func(t *testing.T) {
			got, ok := beastRemainingTitleView(row.raw, row.year)
			if !ok || got != row.title {
				t.Fatalf("%q %v", got, ok)
			}
		})
	}
	controls := []struct {
		name, raw string
		year      int
	}{
		{"unknown-bracket", strings.Replace(bracket, "SweetSub", "Unknown", 1), 0},
		{"unknown-japanese-prefix", strings.Replace(japanese, "Seed-Raws", "Unknown", 1), 0},
		{"unknown-relay", strings.Replace(compound, "ultimafrontiera", "Unknown", 1), 2005},
		{"compound-unknown-extra", compound + "[Unknown]", 2005},
		{"compound-source-mixed", strings.Replace(compound, "BluRay Rip", "BluRay Rip WEB-DL", 1), 2005},
		{"bracket-source-mixed", strings.Replace(bracket, "[BDRip]", "[BDRip WEB-DL]", 1), 0},
		{"japanese-source-mixed", strings.Replace(japanese, "(BD ", "(BD WEB-DL ", 1), 0},
		{"russian-source-mixed", strings.Replace(russian, "BDRip", "BDRip WEB-DL", 1), 1992},
		{"bracket-metadata-missing", strings.Replace(bracket, "[Movie]", "", 1), 0},
		{"bracket-extra", bracket + "[Unknown]", 0},
		{"unbalanced", strings.Replace(russian, ") 1992", " 1992", 1), 1992},
		{"non-cyrillic-prefix", strings.Replace(russian, "Локальное имя", "Localized title", 1), 1992},
		{"role-credit", strings.Replace(russian, "English Alias", "Directed by Someone", 1), 1992},
		{"year-mismatch", russian, 1993}, {"yearless-compound", compound, 0},
		{"parent-path", "/movies/" + russian, 1992}, {"windows-path", `C:\movies\` + russian, 1992},
		{"line-break", bracket + "\n", 0},
	}
	for _, base := range []struct {
		name, raw string
		year      int
	}{{"russian", russian, 1992}, {"bracket", bracket, 0}, {"japanese", japanese, 0}, {"compound", compound, 2005}} {
		for _, marker := range []string{"S01E02", "E03", "1x02", "Complete", "OVA", "Sample", "1999", "../Alias", "Alias//Two", "24/7", "Alias / "} {
			controls = append(controls, struct {
				name, raw string
				year      int
			}{base.name + "-" + marker, strings.Replace(base.raw, "English Alias", marker, 1), base.year})
		}
		controls = append(controls, struct {
			name, raw string
			year      int
		}{base.name + "-mixed-prefix", "S01E02 " + base.raw, base.year})
	}
	for _, c := range controls {
		t.Run(c.name, func(t *testing.T) {
			if got, ok := beastRemainingTitleView(c.raw, c.year); ok {
				t.Fatalf("unexpected view %q", got)
			}
			if _, ok := beastRemainingRelayGroup(c.raw, c.year); ok {
				t.Fatal("unexpected relay repair")
			}
		})
	}
	if got, ok := beastRemainingRelayGroup(compound, 2005); !ok || got != "TEAM" {
		t.Fatalf("relay %q %v", got, ok)
	}
}

func TestBeastRemainingDimensionEvidenceDoesNotInventScanMode(t *testing.T) {
	for _, input := range []string{"1280x720", "1920x1080", "1920x960", "1280x544", "1080i", "720i", "720p 1080i", "1080p 720p", "Resolution1920x1080"} {
		t.Run(input, func(t *testing.T) {
			if got := quality.ParseResolution(input); got != "" {
				t.Fatalf("invented progressive resolution %q", got)
			}
		})
	}
}

func TestBeastRemainingRawCoverageSourceYearGuards(t *testing.T) {
	for _, raw := range []string{
		"S01E02 [SweetSub][English Alias / Alias Two][Movie][BDRip][1080P][AVC 8bit][简体内嵌]",
		"[SweetSub][English Alias S01E02][Movie][BDRip][1080P][AVC 8bit][简体内嵌]",
		"[Seed-Raws] 劇場版 日本語 English Alias S01E02 (BD 1280x720 AVC AACx4 [5.1+2.0+2.0+2.0]).mp4",
	} {
		got := RecognizeRelease(raw)
		for _, key := range got.UnitKeys {
			if key == "movie" {
				t.Fatalf("mixed coverage authorized movie: %+v", got)
			}
		}
	}
	got := RecognizeRelease("Локальное имя (English Alias 1999) 1992 BDRip 1080p.mkv")
	if got.Year != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 {
		t.Fatalf("competing year: %+v", got)
	}
	if got := extractQuality("Alias.1992.BDRip.WEB-DL.1080p"); got.Source != "" {
		t.Fatalf("mixed source: %+v", got)
	}
}
