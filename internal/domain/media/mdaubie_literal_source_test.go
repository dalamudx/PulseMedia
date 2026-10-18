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

var mdaubieVariable = regexp.MustCompile(`(?m)^        String file([1-5]) = ("(?:\\.|[^"\\])*");$`)
var mdaubieAssertion = regexp.MustCompile(`Arguments\.of\(file([1-5]), ParsedFilename\.get\(([^\n]*)\)\)`)
var mdaubieArgument = regexp.MustCompile(`"(?:\\.|[^"\\])*"|null`)

func mdaubieOriginalIndex() (map[string]map[string]json.RawMessage, error) {
	manifest, err := os.ReadFile("../../../testdata/releases/review-inputs/mdaubie-parser-original.manifest.json")
	if err != nil {
		return nil, err
	}
	var m struct {
		Source    string `json:"source_id"`
		Status    string `json:"review_status"`
		Reviewed  int    `json:"reviewed_entries"`
		Accepted  *bool  `json:"accepted"`
		Authority *bool  `json:"authority"`
		Gold      *bool  `json:"formal_golden_acceptance"`
		Coverage  *bool  `json:"coverage_authority"`
		Units     *bool  `json:"canonical_unit_authorization"`
	}
	if err = json.Unmarshal(manifest, &m); err != nil {
		return nil, err
	}
	pending := m.Status == "pending_literal_source_census" && m.Reviewed == 0
	reviewed := m.Status == "partially_reviewed_literal_source" && m.Reviewed == 5
	if m.Source != "mdaubie-parser-original" || !pending && !reviewed {
		return nil, fmt.Errorf("source review status changed")
	}
	for _, flag := range []*bool{m.Accepted, m.Authority, m.Gold, m.Coverage, m.Units} {
		if flag == nil || *flag {
			return nil, fmt.Errorf("source cannot gain authority")
		}
	}
	var source []byte
	for _, f := range []struct{ path, hash string }{
		{"review-inputs/mdaubie-parser-original.LICENSE", "2565ca8bb69ecfef1e9cee779396c359cd711616c15f9c2f198f5dcf385c60be"},
		{"upstream-mit/mdaubie-parser-original.ParserTest.java.source", "3527ae8481fbaa741bba3ecb954198a2387bba6ec86c972935b9858f3792c1b3"},
		{"upstream-mit/mdaubie-model-original.ParsedFilename.java.source", "edc62e88a09e4c3d3759321480b3d0eed2be654f1d2142e3be898fc56ce25ea3"},
		{"upstream-mit/mdaubie-model-original.ParsedMovieFilename.java.source", "1dd9100057abf0d70c6b71e8ed069c8d98f13047af6c8f4dee6870cd2b7de213"},
		{"upstream-mit/mdaubie-model-original.ParsedSeriesEpisodeFilename.java.source", "30209f1c38e0c2920def7f79ec172939397814b286529171712f4a35c6315a06"},
	} {
		raw, e := os.ReadFile("../../../testdata/releases/" + f.path)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != f.hash {
			return nil, fmt.Errorf("fixed original changed: %s", f.path)
		}
		if strings.Contains(f.path, "ParserTest") {
			source = raw
		}
	}
	if !bytes.Contains(source, []byte("assertEquals(actualResult, expectedResult)")) {
		return nil, fmt.Errorf("missing original assertion")
	}
	variables := map[string]string{}
	for _, v := range mdaubieVariable.FindAllSubmatch(source, -1) {
		var input string
		if json.Unmarshal(v[2], &input) != nil || input == "" || variables[string(v[1])] != "" {
			return nil, fmt.Errorf("invalid literal input")
		}
		variables[string(v[1])] = input
	}
	out := map[string]map[string]json.RawMessage{}
	for _, a := range mdaubieAssertion.FindAllSubmatch(source, -1) {
		input, ok := variables[string(a[1])]
		if !ok || out[input] != nil {
			return nil, fmt.Errorf("unbound or duplicate original")
		}
		labels, e := mdaubieLiteralFields(string(a[2]))
		if e != nil {
			return nil, e
		}
		out[input] = labels
	}
	if len(out) != 5 || len(variables) != 5 {
		return nil, fmt.Errorf("original assertion capacity changed")
	}
	return out, nil
}

func mdaubieLiteralFields(text string) (map[string]json.RawMessage, error) {
	args := mdaubieArgument.FindAllString(text, -1)
	if len(args) != 12 || strings.Join(args, ",") != strings.ReplaceAll(text, ", ", ",") {
		return nil, fmt.Errorf("only complete literal argument lists accepted")
	}
	keys := []string{"title", "season", "episode", "year", "release", "resolution", "quality", "codec", "audio", "group", "language", "extension"}
	labels := map[string]json.RawMessage{}
	for i, key := range keys {
		labels[key] = json.RawMessage(args[i])
	}
	var title string
	if json.Unmarshal(labels["title"], &title) != nil || title == "" {
		return nil, fmt.Errorf("missing independent title")
	}
	for _, key := range []string{"season", "episode", "year"} {
		if bytes.Equal(labels[key], []byte("null")) {
			continue
		}
		var value string
		if json.Unmarshal(labels[key], &value) != nil {
			return nil, fmt.Errorf("invalid numbering")
		}
		n, e := strconv.Atoi(value)
		if e != nil || n < 1 || (key == "year" && (n < 1900 || n > 2100)) {
			return nil, fmt.Errorf("invalid original %s", key)
		}
	}
	if bytes.Equal(labels["season"], []byte("null")) != bytes.Equal(labels["episode"], []byte("null")) {
		return nil, fmt.Errorf("incomplete standard numbering")
	}
	return labels, nil
}

func originalMdaubieReviewLabels(input string) (*corpusEntry, error) {
	index, err := mdaubieOriginalIndex()
	if err != nil {
		return nil, err
	}
	labels, ok := index[input]
	if !ok {
		return nil, fmt.Errorf("not an original positive assertion")
	}
	view := map[string]json.RawMessage{}
	for _, key := range []string{"title", "resolution", "codec", "group", "quality"} {
		if !bytes.Equal(labels[key], []byte("null")) {
			view[key] = labels[key]
		}
	}
	for _, key := range []string{"season", "episode", "year"} {
		if bytes.Equal(labels[key], []byte("null")) {
			continue
		}
		var s string
		_ = json.Unmarshal(labels[key], &s)
		n, _ := strconv.Atoi(s)
		if key == "year" {
			view[key], _ = json.Marshal(n)
		} else {
			view[key+"s"], _ = json.Marshal([]int{n})
		}
	}
	if bytes.Equal(view["quality"], []byte(`"webrip"`)) {
		view["quality"] = json.RawMessage(`"WEBRip"`)
	}
	if raw, ok := view["codec"]; ok {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		view["codec"], _ = json.Marshal(strings.ToLower(value))
	}
	out, err := mappedPTTMainReviewLabels(view, input)
	if err != nil {
		return nil, err
	}
	for key, value := range labels {
		out.Expected["mdaubie_original_"+key] = append(json.RawMessage(nil), value...)
	}
	return out, nil
}

func TestMdaubieFixedLiteralAssertionsKeepIndependentFields(t *testing.T) {
	index, err := mdaubieOriginalIndex()
	if err != nil {
		t.Fatal(err)
	}
	for input, labels := range index {
		entry, e := originalMdaubieReviewLabels(input)
		if e != nil {
			t.Fatal(e)
		}
		for key, value := range labels {
			if !bytes.Equal(entry.Expected["mdaubie_original_"+key], value) {
				t.Fatal("original field changed", input, key)
			}
		}
	}
	if _, e := originalMdaubieReviewLabels("README.txt"); e == nil {
		t.Fatal("negative case admitted")
	}
}

func TestMdaubieLiteralArgumentsRejectExpressionsAndPartialCoverage(t *testing.T) {
	good := `"Title","1","2",null,null,null,null,null,null,null,null,"mkv"`
	for _, bad := range []string{strings.Replace(good, `"Title"`, `getTitle()`, 1), strings.Replace(good, `"1"`, `"1.5"`, 1), strings.Replace(good, `"2"`, `null`, 1), strings.Replace(good, `"2"`, `"0"`, 1), good + `,null`} {
		if _, e := mdaubieLiteralFields(bad); e == nil {
			t.Fatal("nonliteral or partial original admitted", bad)
		}
	}
}
