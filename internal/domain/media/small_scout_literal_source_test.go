package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// These inert MIT originals are evidence, not executable fixtures or generated
// parser snapshots. The inventory stays unreviewed and grants no authority.
const smallScoutRoot = "../../../testdata/releases/review-inputs/scout-hellberg-singers-husar-esparza-20261013/"

type smallScoutRow struct {
	Input     string `json:"input"`
	Title     string `json:"title"`
	Source    string `json:"source_id"`
	Index     int    `json:"source_index"`
	InputLine int    `json:"input_line"`
	TitleLine int    `json:"title_line"`
}

type smallScoutAssertion struct {
	Field      string `json:"field"`
	Expression string `json:"expression"`
	Line       int    `json:"line"`
}

func smallScoutPinned(path, hash string) ([]byte, error) {
	raw, err := os.ReadFile(smallScoutRoot + path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("small scout fixed bytes changed: %s", path)
	}
	return raw, nil
}

func smallScoutInventory() ([]smallScoutRow, error) {
	raw, err := smallScoutPinned("inventory.json", "58da160e17a6832a1e3024d52b82d123b7ff6635e5b4b765cfef19f7068baa12")
	if err != nil {
		return nil, err
	}
	if _, err = smallScoutPinned("manifest.json", "a70b20a00b1a6d7491cc5b9ec4f7421345ac3d72a789f3802e6d651b27d6c421"); err != nil {
		return nil, err
	}
	var p struct {
		Rows      []smallScoutRow `json:"rows"`
		Reviewed  int             `json:"reviewed_entries"`
		Accepted  bool            `json:"accepted"`
		Authority bool            `json:"authority"`
		Gold      bool            `json:"formal_golden_acceptance"`
		Coverage  bool            `json:"coverage_authority"`
		Units     bool            `json:"canonical_unit_authorization"`
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Rows) != 126 || p.Reviewed != 0 || p.Accepted || p.Authority || p.Gold || p.Coverage || p.Units {
		return nil, fmt.Errorf("small scout inventory acquired authority or changed capacity")
	}
	return p.Rows, nil
}

// smallScoutString accepts only an original quoted scalar; this is not JS
// evaluation. The one escaped curly apostrophe in the pinned husar source is
// preserved literally, rather than normalized to an ASCII apostrophe.
func smallScoutString(s string) (string, error) {
	if len(s) < 2 || (s[0] != '\'' && s[0] != '"') || s[len(s)-1] != s[0] {
		return "", fmt.Errorf("not a quoted original scalar: %s", s)
	}
	text := s[1 : len(s)-1]
	text = strings.NewReplacer(`\’`, "’", `\'`, "'", `\"`, `"`, `\\`, `\`).Replace(text)
	if strings.Contains(text, `\`) {
		return "", fmt.Errorf("unaudited original escape")
	}
	return text, nil
}

func smallScoutOriginalValue(expression string) (json.RawMessage, error) {
	if expression == "null" {
		return json.RawMessage("null"), nil
	}
	if expression == "void 0" || expression == "dlshow.ParseTypeDate" {
		b, _ := json.Marshal(map[string]string{"expression": expression})
		return b, nil
	}
	if strings.HasPrefix(expression, "'") || strings.HasPrefix(expression, `"`) {
		value, err := smallScoutString(expression)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(value)
		return b, nil
	}
	n, err := strconv.Atoi(expression)
	if err != nil || n < 0 {
		return nil, fmt.Errorf("nonliteral original scalar: %s", expression)
	}
	return json.RawMessage(strconv.Itoa(n)), nil
}

func originalSmallScoutReviewLabels(source, input string) (*corpusEntry, error) {
	rows, err := smallScoutInventory()
	if err != nil {
		return nil, err
	}
	var row *smallScoutRow
	for i := range rows {
		if rows[i].Source == source && rows[i].Input == input {
			if row != nil {
				return nil, fmt.Errorf("duplicate original input")
			}
			row = &rows[i]
		}
	}
	if row == nil {
		return nil, fmt.Errorf("not an original small scout positive title assertion")
	}
	var files []struct{ name, hash string }
	switch source {
	case "singers-scene-release":
		files = []struct{ name, hash string }{{source + ".test.source", "6c1ea498d143d480945549b94fbfc6280f071ae536fcd7699350ba240105a1b6"}, {source + ".LICENSE", "3d6c5f0286590b529c2647657e121d5f2d294c8867a9b6acd94476e1895c253a"}, {source + ".README.source", "1252428333c01e91efce18f01d752cdb497cd7ab892042f94bd15993c3c5f296"}}
	case "husar-movie-title":
		files = []struct{ name, hash string }{{source + ".test.source", "63ea882c452954eb1dc41323079ace4c965005b0cfe21d4bb16228d3bf4423ac"}, {source + ".LICENSE", "97f6c80a9d90040ec13e5fc71cccae1854e1568416479a5eb2ed17f7fc94e2c6"}, {source + ".README.source", "41c1c79d420b0ec37399deae06a0019fddf526168b5fc7dfd5cde88fbdc6ba0d"}}
	case "esparza-dlshow":
		files = []struct{ name, hash string }{{source + ".test.source", "a7f13635679a58b431628ab544c90b65905144b6413efc07ff514fe0f0835ebf"}, {source + ".LICENSE", "f456110687fd203c2f076933c4187b2de9f0af1b75593cc40860bfa2f926ebe8"}, {source + ".README.source", "a9d231487e54e3fc21807bda406a6aee16c95849e2abfa1e1320bef61a881ebd"}}
	default:
		return nil, fmt.Errorf("small scout source outside assigned 41")
	}
	var raw []byte
	for i, f := range files {
		b, e := smallScoutPinned(f.name, f.hash)
		if e != nil {
			return nil, e
		}
		if i == 0 {
			raw = b
		}
		if i == 1 && (!bytes.Contains(b, []byte("Permission is hereby granted")) || !bytes.Contains(b, []byte("Copyright"))) {
			return nil, fmt.Errorf("missing MIT notice")
		}
	}
	lines := strings.Split(string(raw), "\n")
	if row.InputLine < 1 || row.TitleLine < row.InputLine || row.TitleLine > len(lines) {
		return nil, fmt.Errorf("invalid original line binding")
	}
	fields := map[string]json.RawMessage{}
	assertions := []smallScoutAssertion{}
	add := func(field, expression string, line int) error {
		if _, exists := fields[field]; exists {
			return fmt.Errorf("duplicate original field %s", field)
		}
		value, e := smallScoutOriginalValue(expression)
		if e != nil {
			return e
		}
		fields[field] = value
		assertions = append(assertions, smallScoutAssertion{field, expression, line})
		return nil
	}
	start, end := row.InputLine, row.TitleLine
	switch source {
	case "singers-scene-release":
		m := regexp.MustCompile(`^\s*var result = sceneRelease\(('.*')\);$`).FindStringSubmatch(lines[start-1])
		if len(m) != 2 {
			return nil, fmt.Errorf("missing literal sceneRelease input")
		}
		value, e := smallScoutString(m[1])
		if e != nil || value != input {
			return nil, fmt.Errorf("sceneRelease input changed")
		}
		if e = add("input", m[1], start); e != nil {
			return nil, e
		}
		re := regexp.MustCompile(`^\s*assert\.equal\(result\.([A-Za-z]+(?:\[0\])?), (.*)\);$`)
		for n := start; n < len(lines); n++ {
			if strings.TrimSpace(lines[n]) == "});" {
				end = n + 1
				break
			}
			if strings.TrimSpace(lines[n]) == "" {
				continue
			}
			a := re.FindStringSubmatch(lines[n])
			if len(a) != 3 {
				return nil, fmt.Errorf("unrecognized original scene assertion at %d", n+1)
			}
			if e = add(a[1], a[2], n+1); e != nil {
				return nil, e
			}
		}
		if len(fields) != 13 || end <= row.TitleLine {
			return nil, fmt.Errorf("incomplete original scene fields")
		}
	case "husar-movie-title":
		m := regexp.MustCompile(`^\s*assert\.equal\(movieTitle\(('.*')\), ('.*')\);$`).FindStringSubmatch(lines[start-1])
		if len(m) != 3 || row.TitleLine != start {
			return nil, fmt.Errorf("not a direct literal movieTitle assertion")
		}
		value, e := smallScoutString(m[1])
		if e != nil || value != input {
			return nil, fmt.Errorf("movieTitle input changed")
		}
		if e = add("input", m[1], start); e != nil {
			return nil, e
		}
		if e = add("title", m[2], start); e != nil {
			return nil, e
		}
	case "esparza-dlshow":
		if row.Index < 2 {
			m := regexp.MustCompile(`^\s*\{("[^"]+"), ("[^"]+"), ([0-9]+), ([0-9]+), ([0-9]+)\},$`).FindStringSubmatch(lines[start-1])
			if len(m) != 6 {
				return nil, fmt.Errorf("missing complete original dlshow table")
			}
			value, e := smallScoutString(m[1])
			if e != nil || value != input {
				return nil, fmt.Errorf("dlshow input changed")
			}
			for i, key := range []string{"filename", "expectedShowName", "expectedSeasonNumber", "expectedEpisodeNumber", "parseType"} {
				if e = add(key, m[i+1], start); e != nil {
					return nil, e
				}
			}
		} else {
			m := regexp.MustCompile(`^\s*filename := ("[^"]+")$`).FindStringSubmatch(lines[start-1])
			if len(m) != 2 {
				return nil, fmt.Errorf("missing original date input")
			}
			value, e := smallScoutString(m[1])
			if e != nil || value != input {
				return nil, fmt.Errorf("dlshow date input changed")
			}
			if e = add("filename", m[1], start); e != nil {
				return nil, e
			}
			re := regexp.MustCompile(`^\s*if showInfo\.([A-Za-z]+) != (.+) \{$`)
			for n := start; n < len(lines); n++ {
				if a := re.FindStringSubmatch(lines[n]); len(a) == 3 {
					if e = add(a[1], a[2], n+1); e != nil {
						return nil, e
					}
				}
			}
			end = len(lines) - 1
			if len(fields) != 5 {
				return nil, fmt.Errorf("incomplete original date assertions")
			}
		}
	}
	var title string
	titleField := "title"
	if source == "esparza-dlshow" {
		titleField = "expectedShowName"
		if row.Index == 2 {
			titleField = "ShowName"
		}
	}
	if json.Unmarshal(fields[titleField], &title) != nil || title != row.Title {
		return nil, fmt.Errorf("independent original title changed")
	}
	labels := map[string]json.RawMessage{}
	put := func(key string, value any) { labels[key], _ = json.Marshal(value) }
	put("title", title)
	if source == "singers-scene-release" {
		str := func(key string) string { var s string; _ = json.Unmarshal(fields[key], &s); return s }
		if y := str("year"); y != "" {
			n, e := strconv.Atoi(y)
			if e != nil {
				return nil, e
			}
			put("year", n)
		}
		if s := str("resolution"); s != "" {
			put("screen_size", s)
		}
		put("group", str("group"))
		switch s := str("type"); s {
		case "BluRay", "BLURAY":
			put("source", "Blu-ray")
		case "WEB-DL", "WEBRip":
			put("source", "Web")
		case "HDTV":
			put("source", "HDTV")
		case "DVDR", "DVDRip":
			put("source", "DVD")
		default:
			return nil, fmt.Errorf("unaudited original source %s", s)
		}
		switch s := str("video"); s {
		case "H.264", "H 264", "x264":
			put("video_codec", "H.264")
		case "", "NTSC": // Absent codec and NTSC standard do not assert a video codec.
		default:
			return nil, fmt.Errorf("unaudited original video scalar %s", s)
		}
	}
	if source == "esparza-dlshow" && row.Index < 2 {
		labels["season"] = fields["expectedSeasonNumber"]
		labels["episode"] = fields["expectedEpisodeNumber"]
	}
	put("small_scout_original_fields", fields)
	put("small_scout_original_assertions", assertions)
	put("small_scout_original_source_lines", lines[start-1:end])
	put("small_scout_original_line_start", start)
	put("small_scout_original_input_line", row.InputLine)
	put("small_scout_original_title_line", row.TitleLine)
	put("small_scout_original_index", row.Index)
	put("small_scout_original_source", source)
	// Negative evidence only: COMPLETE never establishes any full/partial unit.
	put("small_scout_complete_declaration", regexp.MustCompile(`(?i)\bcomplete\b`).MatchString(input))
	return &corpusEntry{Name: input, Expected: labels}, nil
}

func validateSmallScoutOriginalRebuttals(original *corpusEntry, expected json.RawMessage) error {
	var complete bool
	if err := json.Unmarshal(original.Expected["small_scout_complete_declaration"], &complete); err != nil {
		return err
	}
	if !complete {
		return nil
	}
	var v struct {
		Season     int      `json:"season"`
		Episodes   []int    `json:"episodes"`
		Keys       []string `json:"unit_keys"`
		Absolute   int      `json:"absolute_episode"`
		Confidence float64  `json:"confidence"`
		Review     bool     `json:"requires_review"`
	}
	if err := json.Unmarshal(expected, &v); err != nil {
		return err
	}
	if v.Season != 0 || len(v.Episodes) != 0 || len(v.Keys) != 0 || v.Absolute != 0 || v.Confidence != 0 || !v.Review {
		return fmt.Errorf("original COMPLETE declaration cannot acquire coverage/confidence")
	}
	return nil
}

func TestSmallScoutPinnedOriginalsKeepAllFieldsAndLines(t *testing.T) {
	rows, err := smallScoutInventory()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, row := range rows {
		if row.Source == "hellberg-release" {
			continue
		}
		original, e := originalSmallScoutReviewLabels(row.Source, row.Input)
		if e != nil {
			t.Fatal(row.Source, row.Index, e)
		}
		var fields map[string]json.RawMessage
		if e = json.Unmarshal(original.Expected["small_scout_original_fields"], &fields); e != nil {
			t.Fatal(e)
		}
		want := 2
		if row.Source == "singers-scene-release" {
			want = 13
		}
		if row.Source == "esparza-dlshow" {
			want = 5
		}
		if len(fields) != want {
			t.Fatal("original field count", row.Source, row.Index, len(fields))
		}
		counts[row.Source]++
		if bytes.Equal(original.Expected["small_scout_complete_declaration"], []byte("true")) {
			if e = validateSmallScoutOriginalRebuttals(original, json.RawMessage(`{"season":0,"episodes":[],"unit_keys":["movie"],"absolute_episode":0,"confidence":0.9,"requires_review":false}`)); e == nil {
				t.Fatal("COMPLETE gained authority")
			}
			if e = validateSmallScoutOriginalRebuttals(original, json.RawMessage(`{"season":0,"episodes":[],"unit_keys":[],"absolute_episode":0,"confidence":0,"requires_review":true}`)); e != nil {
				t.Fatal(e)
			}
		}
	}
	if counts["singers-scene-release"] != 14 || counts["husar-movie-title"] != 24 || counts["esparza-dlshow"] != 3 {
		t.Fatal("assigned capacity changed", counts)
	}
	for _, bad := range [][2]string{{"hellberg-release", rows[0].Input}, {"singers-scene-release", "README.txt"}, {"esparza-dlshow", "PBS.The.Civil.War.by.Ken.Burns.1of9.The.Cause.DVDRip.x264.AAC.MVGroup.org.mkv"}} {
		if _, e := originalSmallScoutReviewLabels(bad[0], bad[1]); e == nil {
			t.Fatal("unassigned or negative title admitted", bad)
		}
	}
}
