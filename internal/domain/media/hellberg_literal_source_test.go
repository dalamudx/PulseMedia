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
	"strconv"
	"strings"
	"testing"
)

// Peter Hellberg's MIT notice is preserved in hellberg-release.LICENSE. These
// inert fixed-commit assertions are read as syntax, never imported or executed.
// Registration in the shared gold/checker is intentionally a separate step.
const hellbergRoot = "../../../testdata/releases/review-inputs/scout-hellberg-singers-husar-esparza-20261013/"
const hellbergCommit = "681f91c2b326b7722fbae94f0ae27301c6df05f9"

var hellbergFieldNames = []string{"Input", "Title", "Year", "Season", "Episode", "Type", "Category", "Resolution", "Format", "Audio", "Group"}

func hellbergOriginalIndex() (map[string]map[string]json.RawMessage, error) {
	var source []byte
	for _, f := range []struct{ name, hash string }{
		{"hellberg-release.test.source", "722ad3e6286dc47f2473f0ea2a1c938e75df9044751233eaa42bd8c908629941"},
		{"hellberg-release.LICENSE", "8a9c15bd36fc2962d98036b33e1455d293cd504691530014f35c5a5629fce3ca"},
		{"hellberg-release.README.source", "541c7f5e13cba43dfdba1ff2ec925ab9a2b0df7f5851875a71fef709ecf94ba5"},
		{"manifest.json", "a70b20a00b1a6d7491cc5b9ec4f7421345ac3d72a789f3802e6d651b27d6c421"},
		{"inventory.json", "58da160e17a6832a1e3024d52b82d123b7ff6635e5b4b765cfef19f7068baa12"},
	} {
		raw, err := os.ReadFile(hellbergRoot + f.name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != f.hash {
			return nil, fmt.Errorf("Hellberg fixed original changed: %s", f.name)
		}
		if f.name == "hellberg-release.test.source" {
			source = raw
		}
	}
	for _, field := range hellbergFieldNames {
		if !bytes.Contains(source, []byte("r."+field+" != tt."+field)) {
			return nil, fmt.Errorf("missing original %s assertion", field)
		}
	}
	index, err := decodeHellbergLiterals(source)
	if err != nil {
		return nil, err
	}
	if len(index) != 85 {
		return nil, fmt.Errorf("Hellberg original capacity changed: %d", len(index))
	}
	return index, nil
}

func decodeHellbergLiterals(source []byte) (map[string]map[string]json.RawMessage, error) {
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "release_test.go.source", source, 0)
	if err != nil {
		return nil, err
	}
	var table *ast.CompositeLit
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].Name != "releaseTests" {
				continue
			}
			if table != nil || len(v.Values) != 1 {
				return nil, fmt.Errorf("duplicate or nonliteral releaseTests")
			}
			table, ok = v.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, fmt.Errorf("nonliteral releaseTests")
			}
		}
	}
	if table == nil {
		return nil, fmt.Errorf("missing original table")
	}
	array, ok := table.Type.(*ast.ArrayType)
	if !ok || array.Len != nil {
		return nil, fmt.Errorf("not an original slice")
	}
	fields, ok := array.Elt.(*ast.StructType)
	if !ok || len(fields.Fields.List) != len(hellbergFieldNames) {
		return nil, fmt.Errorf("original field schema changed")
	}
	for i, field := range fields.Fields.List {
		kind, ok := field.Type.(*ast.Ident)
		want := "string"
		if i >= 2 && i <= 4 {
			want = "int"
		}
		if !ok || len(field.Names) != 1 || field.Names[0].Name != hellbergFieldNames[i] || kind.Name != want {
			return nil, fmt.Errorf("original field order/type changed")
		}
	}
	lines := strings.Split(string(source), "\n")
	index := map[string]map[string]json.RawMessage{}
	for row, element := range table.Elts {
		literal, ok := element.(*ast.CompositeLit)
		if !ok || literal.Type != nil || len(literal.Elts) != 11 {
			return nil, fmt.Errorf("incomplete literal row %d", row)
		}
		labels := map[string]json.RawMessage{}
		for i, value := range literal.Elts {
			basic, ok := value.(*ast.BasicLit)
			if !ok {
				return nil, fmt.Errorf("expression is not an original literal")
			}
			if i >= 2 && i <= 4 {
				if basic.Kind != token.INT {
					return nil, fmt.Errorf("noninteger original numbering")
				}
				n, e := strconv.Atoi(basic.Value)
				if e != nil || n < 0 || (i == 2 && n != 0 && (n < 1900 || n > 2100)) {
					return nil, fmt.Errorf("invalid original numbering")
				}
				labels[hellbergFieldNames[i]], _ = json.Marshal(n)
			} else {
				if basic.Kind != token.STRING {
					return nil, fmt.Errorf("nonstring original scalar")
				}
				s, e := strconv.Unquote(basic.Value)
				if e != nil || (i < 2 && s == "") {
					return nil, fmt.Errorf("invalid original string")
				}
				labels[hellbergFieldNames[i]], _ = json.Marshal(s)
			}
		}
		var input string
		_ = json.Unmarshal(labels["Input"], &input)
		if _, exists := index[input]; exists {
			return nil, fmt.Errorf("duplicate original input")
		}
		var season, episode int
		_ = json.Unmarshal(labels["Season"], &season)
		_ = json.Unmarshal(labels["Episode"], &episode)
		if (season == 0) != (episode == 0) {
			return nil, fmt.Errorf("partial original numbering")
		}
		line := fs.Position(literal.Pos()).Line
		if fs.Position(literal.End()).Line != line {
			return nil, fmt.Errorf("original row must remain on one source line")
		}
		labels["source_index"], _ = json.Marshal(row)
		labels["source_line"], _ = json.Marshal(line)
		labels["source_text"], _ = json.Marshal(lines[line-1])
		index[input] = labels
	}
	return index, nil
}

func originalHellbergReviewLabels(input string) (*corpusEntry, error) {
	index, err := hellbergOriginalIndex()
	if err != nil {
		return nil, err
	}
	labels, ok := index[input]
	if !ok {
		return nil, fmt.Errorf("not a pinned Hellberg original")
	}
	view := map[string]json.RawMessage{"title": labels["Title"], "year": labels["Year"]}
	// Zero season/episode are original absence assertions, not S00E00 coverage.
	var season, episode int
	_ = json.Unmarshal(labels["Season"], &season)
	_ = json.Unmarshal(labels["Episode"], &episode)
	if episode > 0 {
		view["season"], _ = json.Marshal([]int{season})
		view["episode"], _ = json.Marshal([]int{episode})
	}
	for from, to := range map[string]string{"Resolution": "screen_size", "Group": "group"} {
		var value string
		_ = json.Unmarshal(labels[from], &value)
		if value != "" {
			view[to] = append(json.RawMessage(nil), labels[from]...)
		}
	}
	var category, codec string
	_ = json.Unmarshal(labels["Category"], &category)
	_ = json.Unmarshal(labels["Format"], &codec)
	if category != "" {
		if category == "Webrip" {
			category = "WEBRip"
		}
		view["ptt_main_quality"], _ = json.Marshal(category)
	}
	if codec != "" {
		view["ptt_main_codec"], _ = json.Marshal(strings.ToLower(codec))
	}
	// Type is an edition claim, Audio is an unmeasured track claim. Neither
	// becomes media type, measured audio, rank, canonical units or permission.
	for key, value := range labels {
		view["hellberg_original_"+key] = append(json.RawMessage(nil), value...)
	}
	view["hellberg_original_commit"], _ = json.Marshal(hellbergCommit)
	return &corpusEntry{Name: input, Expected: view}, nil
}

func TestHellbergFixedLiteralAssertionsKeepIndependentFields(t *testing.T) {
	index, err := hellbergOriginalIndex()
	if err != nil {
		t.Fatal(err)
	}
	for input, labels := range index {
		entry, e := originalHellbergReviewLabels(input)
		if e != nil {
			t.Fatal(e)
		}
		for key, value := range labels {
			if !bytes.Equal(entry.Expected["hellberg_original_"+key], value) {
				t.Fatalf("original field changed: %s", key)
			}
		}
		if !bytes.Equal(entry.Expected["title"], labels["Title"]) || !bytes.Equal(entry.Expected["year"], labels["Year"]) {
			t.Fatal("title or year rewritten")
		}
		if _, exists := entry.Expected["type"]; exists {
			t.Fatal("edition became media type")
		}
		for _, key := range []string{"audio", "edition", "unit_keys", "confidence", "requires_review"} {
			if _, exists := entry.Expected[key]; exists {
				t.Fatalf("invented independent authority: %s", key)
			}
		}
	}
	if _, e := originalHellbergReviewLabels("invented"); e == nil {
		t.Fatal("unknown input admitted")
	}
	input := "The.Mandalorian.S01E01.INTERNAL.1080p.WEB.H264-NOCROP[rartv]"
	entry, err := originalHellbergReviewLabels(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(entry.Expected["title"]) != `"The Mandalorian S01E01 INTERNAL"` || string(entry.Expected["season"]) != `[1]` || string(entry.Expected["episode"]) != `[1]` {
		t.Fatal("pending title/numbering conflict rewritten")
	}
}

func TestHellbergLiteralCountercontrols(t *testing.T) {
	raw, err := os.ReadFile(hellbergRoot + "hellberg-release.test.source")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(string) string{
		"computed title": func(s string) string {
			return strings.Replace(s, `"Act of Valor"`, `strings.TrimSpace("Act of Valor")`, 1)
		},
		"fractional year":  func(s string) string { return strings.Replace(s, `2012, 0, 0`, `2012.5, 0, 0`, 1) },
		"partial coverage": func(s string) string { return strings.Replace(s, `2012, 0, 0`, `2012, 1, 0`, 1) },
		"negative year":    func(s string) string { return strings.Replace(s, `2012, 0, 0`, `-2012, 0, 0`, 1) },
		"duplicate input": func(s string) string {
			return strings.Replace(s, `"American.Reunion.UNRATED.720p.BluRay.X264-BLOW"`, `"Act.of.Valor.2012.720p.BluRay.DTS.x264-HDxT"`, 1)
		},
		"missing field":  func(s string) string { return strings.Replace(s, `"DTS", "HDxT"`, `"DTS"`, 1) },
		"schema reorder": func(s string) string { return strings.Replace(s, `Title      string`, `Other      string`, 1) },
		"empty title":    func(s string) string { return strings.Replace(s, `"Act of Valor"`, `""`, 1) },
		"unbound table":  func(s string) string { return strings.Replace(s, `var releaseTests`, `var unrelated`, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := mutate(string(raw))
			if changed == string(raw) {
				t.Fatal("ineffective countercontrol")
			}
			if _, e := decodeHellbergLiterals([]byte(changed)); e == nil {
				t.Fatal("nonliteral or incomplete evidence admitted")
			}
		})
	}
}
