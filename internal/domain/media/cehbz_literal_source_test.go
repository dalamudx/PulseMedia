package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// MIT cehbz/torrentname@803fda3c3c111ae99bfb8ecd98eaca0719503068.
// Original data and license stay in testdata/releases; no upstream Go is run.
// Confidence expressions are opaque source text, including numeric literals.
const cehbzSourcePath = "testdata/releases/upstream-mit/cehbz-parse-original.parse_test.go.source"
const cehbzSourceSHA = "3848e40b8117f4c447a9f6a350052b5d255c80604d72b73bac04f483878c51df"

type cehbzLiteralPosition struct {
	Line      int `json:"line"`
	EndLine   int `json:"end_line"`
	ByteStart int `json:"byte_start"`
	ByteEnd   int `json:"byte_end"`
}
type cehbzLiteralOccurrence struct {
	CaseName       string                          `json:"case_name"`
	Path           string                          `json:"path"`
	Block          cehbzLiteralPosition            `json:"block"`
	Input          cehbzLiteralPosition            `json:"input"`
	Fields         map[string]cehbzLiteralPosition `json:"fields"`
	RawFields      map[string]string               `json:"raw_fields"`
	AssertionLine  int                             `json:"assertion_call_line"`
	ComparatorLine int                             `json:"title_comparator_line"`
}
type cehbzLiteralRecord struct {
	Labels      map[string]json.RawMessage `json:"labels"`
	FixtureKind string                     `json:"fixture_kind"`
	Occurrences []cehbzLiteralOccurrence   `json:"occurrences"`
}

// Exact input positions classified synthetic_literal by the preserved original
// census; examples and separator/case controls can never become release gold.
func cehbzSyntheticLine(line int) bool {
	return slices.Contains([]int{260, 272, 284, 296, 333, 346, 373, 425, 438, 451, 464, 477, 490, 516, 562, 575, 588, 602, 615, 640, 650, 664}, line)
}

func decodeCehbzLiteralField(key string, expr ast.Expr, raw string) (json.RawMessage, error) {
	if key == "Confidence" {
		b, _ := json.Marshal(raw)
		return b, nil
	}
	switch v := expr.(type) {
	case *ast.BasicLit:
		switch v.Kind {
		case token.STRING:
			s, err := strconv.Unquote(v.Value)
			if err != nil {
				return nil, err
			}
			b, _ := json.Marshal(s)
			return b, nil
		case token.INT:
			// Decimal positive/zero literal only. No unary operators or arithmetic.
			if v.Value == "" || strings.IndexFunc(v.Value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				break
			}
			return json.RawMessage(v.Value), nil
		}
	case *ast.Ident:
		if v.Name == "true" || v.Name == "false" {
			return json.RawMessage(v.Name), nil
		}
	}
	return nil, fmt.Errorf("nonliteral original field %s", key)
}

func cehbzOriginalAssertionIndex() (map[string]cehbzLiteralRecord, error) {
	root := "../../../"
	manifest, err := os.ReadFile(root + "testdata/releases/review-inputs/cehbz-parse-original.manifest.json")
	if err != nil {
		return nil, err
	}
	var m struct {
		ID         string `json:"id"`
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
		License    string `json:"license"`
		Status     string `json:"review_status"`
		Counts     struct {
			Positive  int `json:"positive_literal_assertions"`
			Unique    int `json:"positive_unique_inputs"`
			Synthetic int `json:"synthetic_unique_inputs"`
		} `json:"counts"`
		Flags struct {
			Accepted  *bool `json:"accepted"`
			Authority *bool `json:"authority"`
			Gold      *bool `json:"formal_golden_acceptance"`
			Canonical *bool `json:"canonical_unit_authorization"`
			Coverage  *bool `json:"coverage_authority"`
			Reviewed  int   `json:"reviewed_entries"`
		} `json:"flags"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, err
	}
	// Main may record individual passing reviews; source-wide authority stays false.
	if m.ID != "cehbz-parse-original" || m.Repository != "https://github.com/cehbz/torrentname" || m.Commit != "803fda3c3c111ae99bfb8ecd98eaca0719503068" || m.License != "MIT" || (m.Status != "pending_original_source_census" && m.Status != "partially_reviewed_literal_source") || m.Counts.Positive != 51 || m.Counts.Unique != 47 || m.Counts.Synthetic != 22 || m.Flags.Reviewed < 0 || m.Flags.Reviewed > 25 {
		return nil, fmt.Errorf("original manifest identity/capacity changed")
	}
	for _, flag := range []*bool{m.Flags.Accepted, m.Flags.Authority, m.Flags.Gold, m.Flags.Canonical, m.Flags.Coverage} {
		if flag == nil || *flag {
			return nil, fmt.Errorf("original source cannot gain authority")
		}
	}
	for _, f := range []struct{ path, sha string }{
		{"testdata/releases/review-inputs/cehbz-parse-original.LICENSE", "31aa89620a821a00659bfb779f63b1df54a82e01036569882cd6bbedbf1d0af6"},
		{cehbzSourcePath, cehbzSourceSHA},
	} {
		raw, err := os.ReadFile(root + f.path)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != f.sha {
			return nil, fmt.Errorf("fixed cehbz provenance changed: %s", f.path)
		}
	}
	data, err := os.ReadFile(root + cehbzSourcePath)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, cehbzSourcePath, data, 0)
	if err != nil {
		return nil, err
	}
	pos := func(n ast.Node) cehbzLiteralPosition {
		a, b := fset.Position(n.Pos()), fset.Position(n.End())
		return cehbzLiteralPosition{a.Line, b.Line, a.Offset, b.Offset}
	}
	var body *ast.BlockStmt
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "TestParse" {
			if body != nil {
				return nil, fmt.Errorf("duplicate TestParse")
			}
			body = f.Body
		}
	}
	if body == nil {
		return nil, fmt.Errorf("missing fixed title assertion table")
	}
	var table *ast.CompositeLit
	for _, stmt := range body.List {
		a, ok := stmt.(*ast.AssignStmt)
		if !ok || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
			continue
		}
		id, ok := a.Lhs[0].(*ast.Ident)
		if !ok || id.Name != "tests" {
			continue
		}
		table, ok = a.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("nonliteral original table")
		}
	}
	if table == nil {
		return nil, fmt.Errorf("missing original literal table")
	}
	index := map[string]cehbzLiteralRecord{}
	total, synthetic := 0, 0
	for _, el := range table.Elts {
		block, ok := el.(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("nonliteral original row")
		}
		var input, caseName string
		var inputPos cehbzLiteralPosition
		var expected *ast.CompositeLit
		seen := map[string]bool{}
		for _, item := range block.Elts {
			kv, ok := item.(*ast.KeyValueExpr)
			if !ok {
				return nil, fmt.Errorf("unkeyed original row")
			}
			k, ok := kv.Key.(*ast.Ident)
			if !ok || seen[k.Name] {
				return nil, fmt.Errorf("duplicate/nonliteral row key")
			}
			seen[k.Name] = true
			switch k.Name {
			case "input", "name":
				lit, ok := kv.Value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return nil, fmt.Errorf("computed original input/name")
				}
				s, e := strconv.Unquote(lit.Value)
				if e != nil {
					return nil, e
				}
				if k.Name == "input" {
					input = s
					inputPos = pos(kv.Value)
				} else {
					caseName = s
				}
			case "expected":
				u, ok := kv.Value.(*ast.UnaryExpr)
				if !ok || u.Op != token.AND {
					return nil, fmt.Errorf("nonliteral original expected")
				}
				expected, ok = u.X.(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("computed expected")
				}
			default:
				return nil, fmt.Errorf("unknown original row key")
			}
		}
		if input == "" || caseName == "" || expected == nil {
			return nil, fmt.Errorf("incomplete original row")
		}
		labels := map[string]json.RawMessage{}
		occurrence := cehbzLiteralOccurrence{CaseName: caseName, Path: cehbzSourcePath, Block: pos(block), Input: inputPos, Fields: map[string]cehbzLiteralPosition{}, RawFields: map[string]string{}, AssertionLine: 679, ComparatorLine: 693}
		for _, item := range expected.Elts {
			kv, ok := item.(*ast.KeyValueExpr)
			if !ok {
				return nil, fmt.Errorf("unkeyed expected field")
			}
			k, ok := kv.Key.(*ast.Ident)
			if !ok {
				return nil, fmt.Errorf("computed field key")
			}
			if _, ok := labels[k.Name]; ok {
				return nil, fmt.Errorf("duplicate original field")
			}
			p := pos(kv.Value)
			raw := string(data[p.ByteStart:p.ByteEnd])
			value, e := decodeCehbzLiteralField(k.Name, kv.Value, raw)
			if e != nil {
				return nil, e
			}
			labels[k.Name] = value
			occurrence.Fields[k.Name] = p
			occurrence.RawFields[k.Name] = raw
		}
		if _, err := mappedCehbzReviewLabels(labels, input); err != nil {
			return nil, err
		}
		kind := "release_literal_unverified_independence"
		if cehbzSyntheticLine(inputPos.Line) {
			kind = "synthetic_literal"
			synthetic++
		}
		if old, exists := index[input]; exists {
			a, _ := json.Marshal(old.Labels)
			b, _ := json.Marshal(labels)
			if !bytes.Equal(a, b) || old.FixtureKind != kind {
				return nil, fmt.Errorf("conflicting duplicate original assertion")
			}
			old.Occurrences = append(old.Occurrences, occurrence)
			index[input] = old
		} else {
			index[input] = cehbzLiteralRecord{labels, kind, []cehbzLiteralOccurrence{occurrence}}
		}
		total++
	}
	if total != 51 || len(index) != 47 || synthetic != 22 {
		return nil, fmt.Errorf("original census drift: %d/%d/%d", total, len(index), synthetic)
	}
	return index, nil
}

func mappedCehbzReviewLabels(labels map[string]json.RawMessage, input string) (*corpusEntry, error) {
	out := map[string]json.RawMessage{}
	stringsOnly := map[string]bool{"Title": true, "Resolution": true, "Source": true, "Codec": true, "ReleaseGroup": true, "Audio": true, "Edition": true, "Language": true, "Container": true, "Unparsed": true, "Date": true, "Type": true, "Confidence": true}
	bools := map[string]bool{"IsComplete": true, "IsProper": true, "IsRepack": true, "IsHardcoded": true}
	for k, v := range labels {
		if stringsOnly[k] {
			var s string
			if len(v) == 0 || v[0] != '"' || json.Unmarshal(v, &s) != nil || s == "" {
				return nil, fmt.Errorf("invalid original %s", k)
			}
		} else if bools[k] {
			if string(v) != "true" && string(v) != "false" {
				return nil, fmt.Errorf("invalid original bool")
			}
		} else if k == "Year" || k == "Season" || k == "Episode" {
			var n int
			if string(v) == "null" || json.Unmarshal(v, &n) != nil || n < 0 || ((k == "Episode" || k == "Year") && n == 0) {
				return nil, fmt.Errorf("invalid original %s", k)
			}
		} else {
			return nil, fmt.Errorf("unaudited original field %s", k)
		}
		out["cehbz_original_"+k] = slices.Clone(v)
	}
	if _, ok := labels["Title"]; !ok {
		return nil, fmt.Errorf("missing independent title")
	}
	for from, to := range map[string]string{"Title": "title", "Year": "year", "Season": "season", "Episode": "episode", "Resolution": "screen_size", "ReleaseGroup": "group", "Type": "type"} {
		if v, ok := labels[from]; ok {
			out[to] = slices.Clone(v)
		}
	}
	if v, ok := labels["Codec"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		names := map[string]string{"H264": "h264", "H265": "h265"}
		name, ok := names[s]
		if !ok {
			return nil, fmt.Errorf("unaudited codec %q", s)
		}
		out["ptt_main_codec"], _ = json.Marshal(name)
	}
	if v, ok := labels["Source"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		if !slices.Contains([]string{"BluRay", "WEB-DL", "WEBRip", "HDTV", "CAM", "DVD"}, s) {
			return nil, fmt.Errorf("unaudited source %q", s)
		}
		if s == "DVD" {
			s = "DVDRip"
		}
		out["ptt_main_quality"], _ = json.Marshal(s)
	}
	if v, ok := labels["Type"]; ok && string(v) != `"movie"` && string(v) != `"episode"` {
		return nil, fmt.Errorf("invalid original type")
	}
	if string(labels["Type"]) == `"movie"` && (labels["Season"] != nil || labels["Episode"] != nil) {
		return nil, fmt.Errorf("movie contradicts numbering")
	}
	return &corpusEntry{Name: input, Expected: out}, nil
}

func originalCehbzReviewLabels(input string) (*corpusEntry, error) {
	index, err := cehbzOriginalAssertionIndex()
	if err != nil {
		return nil, err
	}
	row, ok := index[input]
	if !ok || row.FixtureKind != "release_literal_unverified_independence" {
		return nil, fmt.Errorf("not a non-synthetic original cehbz title claim")
	}
	return mappedCehbzReviewLabels(row.Labels, input)
}

var cehbzCanonicalNumber = regexp.MustCompile(`(?i)\b(?:S([0-9]{1,2})E([0-9]{1,3})|([0-9]{1,2})x([0-9]{1,3}))\b`)

// Supplied fields are positive, optional counterevidence. An absent field is
// never a negative assertion. No upstream confidence calculation is evaluated.
func validateCehbzOriginalRebuttals(original *corpusEntry, raw json.RawMessage) error {
	if original == nil {
		return fmt.Errorf("missing independent original")
	}
	labels := map[string]json.RawMessage{}
	for k, v := range original.Expected {
		if strings.HasPrefix(k, "cehbz_original_") {
			labels[strings.TrimPrefix(k, "cehbz_original_")] = v
		}
	}
	mapped, err := mappedCehbzReviewLabels(labels, original.Name)
	if err != nil {
		return err
	}
	var w struct {
		Title      string          `json:"title"`
		Year       int             `json:"year"`
		Kind       string          `json:"media_type"`
		Season     int             `json:"season"`
		Episodes   []int           `json:"episodes"`
		Keys       []string        `json:"unit_keys"`
		Absolute   int             `json:"absolute_episode"`
		Anime      bool            `json:"is_anime"`
		Confidence float64         `json:"confidence"`
		Review     bool            `json:"requires_review"`
		Quality    json.RawMessage `json:"quality"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	if string(labels["Title"]) != string(mustCehbzJSON(w.Title)) {
		return fmt.Errorf("original title changed")
	}
	if v, ok := labels["Year"]; ok && string(v) != strconv.Itoa(w.Year) {
		return fmt.Errorf("original supplied year changed")
	}
	if v, ok := labels["Type"]; ok {
		var kind string
		_ = json.Unmarshal(v, &kind)
		if (kind == "movie" && w.Kind != "movie") || (kind == "episode" && w.Kind != "tv") {
			return fmt.Errorf("original type changed")
		}
	}
	var season, episode int
	if v, ok := labels["Season"]; ok {
		_ = json.Unmarshal(v, &season)
	}
	if v, ok := labels["Episode"]; ok {
		_ = json.Unmarshal(v, &episode)
	}
	numbers := cehbzCanonicalNumber.FindAllStringSubmatch(original.Name, -1)
	canonical := len(numbers) == 1 && labels["Season"] != nil && labels["Episode"] != nil
	if canonical {
		m := numbers[0]
		a, b := m[1], m[2]
		if a == "" {
			a, b = m[3], m[4]
		}
		s, _ := strconv.Atoi(a)
		e, _ := strconv.Atoi(b)
		canonical = s == season && e == episode
	}
	if (labels["Season"] != nil || labels["Episode"] != nil) && w.Kind != "tv" {
		return fmt.Errorf("original numbering cannot become a movie")
	}
	unbound := string(labels["IsComplete"]) == "true" || labels["Date"] != nil || ((labels["Season"] != nil || labels["Episode"] != nil || string(labels["Type"]) == `"episode"`) && !canonical)
	if unbound {
		if !w.Review || w.Confidence != 0 || w.Season != 0 || len(w.Episodes) != 0 || len(w.Keys) != 0 || w.Absolute != 0 || w.Anime {
			return fmt.Errorf("original unresolved/complete coverage gained authority")
		}
	} else if canonical {
		if w.Kind != "tv" || w.Season != season || !slices.Equal(w.Episodes, []int{episode}) || !slices.Equal(w.Keys, []string{fmt.Sprintf("S%02dE%02d", season, episode)}) || w.Absolute != 0 || w.Anime {
			return fmt.Errorf("canonical original season/episode changed")
		}
	}
	type plainQuality quality.Profile
	var q plainQuality
	if err := json.Unmarshal(w.Quality, &q); err != nil {
		return err
	}
	if err := validateReviewedQualityLabels(mapped.Expected, quality.Profile(q)); err != nil {
		return err
	}
	if err := validatePTTMainQualityRebuttals(mapped.Expected, quality.Profile(q)); err != nil {
		return err
	}
	if q.Audio != "" || q.Edition != "" {
		return fmt.Errorf("lexical audio/edition is not measured quality")
	}
	return nil
}
func mustCehbzJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestCehbzOriginalSourceIntegrityAndLiteralAdmission(t *testing.T) {
	index, err := cehbzOriginalAssertionIndex()
	if err != nil {
		t.Fatal(err)
	}
	real, synthetic, total := 0, 0, 0
	for input, row := range index {
		total += len(row.Occurrences)
		got, err := originalCehbzReviewLabels(input)
		if row.FixtureKind == "synthetic_literal" {
			synthetic++
			if err == nil {
				t.Fatal("synthetic fixture admitted", input)
			}
			continue
		}
		real++
		if err != nil {
			t.Fatal(input, err)
		}
		for k, v := range row.Labels {
			if !bytes.Equal(v, got.Expected["cehbz_original_"+k]) {
				t.Fatal("original field lost", input, k)
			}
		}
		for _, p := range row.Occurrences {
			if p.Input.Line < 1 || p.Block.Line > p.Input.Line || p.Block.EndLine < p.Fields["Title"].Line || p.Input.ByteEnd <= p.Input.ByteStart || p.AssertionLine != 679 || p.ComparatorLine != 693 {
				t.Fatal("missing exact source position")
			}
		}
	}
	if real != 25 || synthetic != 22 || total != 51 {
		t.Fatal("census is capacity, not gold", real, synthetic, total)
	}
	if _, err := originalCehbzReviewLabels("invented movie"); err == nil {
		t.Fatal("invented input admitted")
	}
}

func TestCehbzOriginalCounterevidenceAndOptionalFields(t *testing.T) {
	labels := map[string]json.RawMessage{"Title": mustCehbzJSON("Show"), "Season": mustCehbzJSON(2), "Episode": mustCehbzJSON(8), "Resolution": mustCehbzJSON("1080p"), "Source": mustCehbzJSON("WEBRip"), "Codec": mustCehbzJSON("H265"), "Type": mustCehbzJSON("episode"), "Confidence": mustCehbzJSON("YearSeasonWeight + unknownCall()")}
	original, err := mappedCehbzReviewLabels(labels, "Show.S02E08")
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]any{"title": "Show", "year": 2020, "media_type": "tv", "season": 2, "episodes": []int{8}, "unit_keys": []string{"S02E08"}, "confidence": .95, "quality": map[string]any{"resolution": "1080p", "source": "WEBRip", "codec": "HEVC", "group": "EXTRA"}}
	if err := validateCehbzOriginalRebuttals(original, mustCehbzJSON(good)); err != nil {
		t.Fatal("absent year/group are not negative", err)
	}
	for k, v := range map[string]any{"title": "Other", "media_type": "movie", "season": 1, "episodes": []int{8, 9}, "unit_keys": []string{"S02E08", "S02E09"}, "absolute_episode": 8, "is_anime": true, "quality": map[string]any{"source": "WEB-DL"}} {
		bad := map[string]any{}
		for a, b := range good {
			bad[a] = b
		}
		bad[k] = v
		if validateCehbzOriginalRebuttals(original, mustCehbzJSON(bad)) == nil {
			t.Fatal("counterevidence ignored", k)
		}
	}
	for _, q := range []map[string]any{{"resolution": "720p"}, {"codec": "H264"}, {"source": "BluRay"}, {"audio": "DTS"}, {"edition": "Extended"}} {
		bad := map[string]any{}
		for k, v := range good {
			bad[k] = v
		}
		bad["quality"] = q
		if validateCehbzOriginalRebuttals(original, mustCehbzJSON(bad)) == nil {
			t.Fatal("supplied quality/unmeasured field ignored", q)
		}
	}
	groupLabels := map[string]json.RawMessage{"Title": mustCehbzJSON("Show"), "ReleaseGroup": mustCehbzJSON("SOURCE")}
	groupOriginal, e := mappedCehbzReviewLabels(groupLabels, "Show")
	if e != nil {
		t.Fatal(e)
	}
	if validateCehbzOriginalRebuttals(groupOriginal, mustCehbzJSON(good)) == nil {
		t.Fatal("supplied group ignored")
	}
	labels["Year"] = mustCehbzJSON(2019)
	original, _ = mappedCehbzReviewLabels(labels, "Show.S02E08")
	if validateCehbzOriginalRebuttals(original, mustCehbzJSON(good)) == nil {
		t.Fatal("supplied year ignored")
	}
	for _, labels := range []map[string]json.RawMessage{
		{"Title": mustCehbzJSON("Show"), "Season": mustCehbzJSON(2)},
		{"Title": mustCehbzJSON("Show"), "Episode": mustCehbzJSON(8)},
		{"Title": mustCehbzJSON("Show"), "IsComplete": mustCehbzJSON(true)},
		{"Title": mustCehbzJSON("Show"), "Type": mustCehbzJSON("episode")},
	} {
		row, e := mappedCehbzReviewLabels(labels, "Show")
		if e != nil {
			t.Fatal(e)
		}
		if validateCehbzOriginalRebuttals(row, mustCehbzJSON(good)) == nil {
			t.Fatal("unbound label gained units")
		}
		refusal := map[string]any{"title": "Show", "media_type": "tv", "confidence": 0, "requires_review": true, "quality": map[string]any{}}
		if e := validateCehbzOriginalRebuttals(row, mustCehbzJSON(refusal)); e != nil {
			t.Fatal(e)
		}
	}
	for _, text := range []string{`f()`, `1+2`, `[]int{1,2}`} {
		expr, e := parser.ParseExpr(text)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := decodeCehbzLiteralField("Season", expr, text); e == nil {
			t.Fatal("computed numbering accepted")
		}
		if v, e := decodeCehbzLiteralField("Confidence", expr, text); e != nil || string(v) != string(mustCehbzJSON(text)) {
			t.Fatal("confidence text evaluated")
		}
	}
	for _, bad := range []map[string]json.RawMessage{{"Title": mustCehbzJSON("Show"), "Season": json.RawMessage(`null`)}, {"Title": mustCehbzJSON("Show"), "Episode": json.RawMessage(`[1,2]`)}, {"Title": mustCehbzJSON("Show"), "Type": mustCehbzJSON("movie"), "Season": mustCehbzJSON(1)}} {
		if _, e := mappedCehbzReviewLabels(bad, "Show"); e == nil {
			t.Fatal("invalid supplied field accepted")
		}
	}
}
