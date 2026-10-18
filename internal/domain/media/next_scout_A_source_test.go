package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const nextScoutARoot = "../../../testdata/releases/review-inputs/new-independent-scout-20261014-mit-assertions/"

type nextScoutARow struct {
	Source    string                     `json:"source_id"`
	File      string                     `json:"source_file"`
	Index     int                        `json:"source_index"`
	Input     string                     `json:"input"`
	Title     string                     `json:"title"`
	InputLine int                        `json:"input_line"`
	TitleLine int                        `json:"title_line"`
	Block     string                     `json:"original_assertion_block"`
	Group     string                     `json:"original_group"`
	Fields    map[string]json.RawMessage `json:"original_expected"`
}

// This reader admits only concat(inventory.rows,supplement.rows)[0,63). It reads
// fixed, inert MIT assertions; it never runs an upstream parser or grants source
// pool authority. Native fields and complete arrays stay in the returned labels.
func originalNextScoutAReviewLabels(source, input string) (*corpusEntry, error) {
	pinned := map[string]string{
		"inventory.json": "fd7e979bd16edaf9cff04f72e85c5c3f70836f8e378a50aee90d59cd8291808a",
		"manifest.json":  "5fb2473c2e5ec255051589fd15fb505ee59d9fb859fba1608c18dbe0a727a74f",
		"sources/walker-media-name-parser/LICENSE":                                "b75bb24a9e75093a8b39187aee08abf7cce8057e1cbb8243eeb2a430e008578f",
		"sources/walker-media-name-parser/test/parse/video.test.ts.source":        "bda9aa8082237ff89dfd7e22079f8b99316f2296be7bf2aa7dc05c179662bb21",
		"sources/walker-media-name-parser/test/parse/leadingGroup.test.ts.source": "beca47090cf7dd927b272259921ddc3895bbcf6f01d9c0f5298586c64ae6632a",
		"sources/machard-releascenify/LICENSE":                                    "fab5cadfd8068b65cc0923b6051f542f2bd34d2f51af15116cde609800d7a836",
		"sources/machard-releascenify/tests/test_cases_movies.json.source":        "20eaadeafbbe65171648f47f0ea6a48d9aea1c9f41518bc5e06efdcd785e1b37",
		"sources/machard-releascenify/tests/test_parser.py.source":                "c9186259ada6d2ab658efed125c012837089e0069d9b6892430aa62450e16fae",
	}
	files := map[string][]byte{}
	for path, want := range pinned {
		raw, err := os.ReadFile(nextScoutARoot + path)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != want {
			return nil, fmt.Errorf("next-A fixed source changed: %s", path)
		}
		files[path] = raw
	}
	for _, s := range []string{"walker-media-name-parser", "machard-releascenify"} {
		if !bytes.Contains(files["sources/"+s+"/LICENSE"], []byte("Permission is hereby granted, free of charge")) {
			return nil, fmt.Errorf("missing complete MIT notice")
		}
	}
	var pool struct {
		Rows []nextScoutARow `json:"rows"`
	}
	if err := json.Unmarshal(files["inventory.json"], &pool); err != nil {
		return nil, err
	}
	if len(pool.Rows) != 117 {
		return nil, fmt.Errorf("changed inventory boundary")
	}
	var found *nextScoutARow
	for i := 0; i < 63; i++ {
		r := pool.Rows[i]
		if r.Source == source && r.Input == input {
			if found != nil {
				return nil, fmt.Errorf("duplicate original input")
			}
			found = &r
		}
	}
	if found == nil {
		return nil, fmt.Errorf("not an original next-A partition input")
	}
	r := *found
	labels := map[string]json.RawMessage{}
	set := func(k string, v any) { labels[k], _ = json.Marshal(v) }
	set("next_scout_a_input", input)
	set("next_scout_a_source", source)
	set("next_scout_a_source_index", r.Index)
	set("next_scout_a_input_line", r.InputLine)
	set("next_scout_a_title_line", r.TitleLine)
	sourcePath := "sources/" + source + "/" + r.File + ".source"
	raw, ok := files[sourcePath]
	if !ok {
		return nil, fmt.Errorf("unexpected original path")
	}
	lines := strings.Split(string(raw), "\n")
	if r.InputLine < 1 || r.TitleLine < 1 || r.InputLine > len(lines) || r.TitleLine > len(lines) {
		return nil, fmt.Errorf("invalid literal location")
	}
	if source == "machard-releascenify" {
		var rows []struct {
			Input  string                     `json:"filename"`
			Fields map[string]json.RawMessage `json:"expected"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
		if r.Index < 0 || r.Index >= len(rows) || rows[r.Index].Input != input {
			return nil, fmt.Errorf("original source index changed")
		}
		if !nextScoutARawFieldsEqual(rows[r.Index].Fields, r.Fields) {
			return nil, fmt.Errorf("original dictionary changed")
		}
		var lineInput, lineTitle string
		for _, pair := range []struct {
			line   int
			target *string
		}{{r.InputLine, &lineInput}, {r.TitleLine, &lineTitle}} {
			line := strings.TrimSpace(lines[pair.line-1])
			line = strings.TrimSuffix(line, ",")
			at := strings.IndexByte(line, ':')
			if at < 0 {
				return nil, fmt.Errorf("invalid JSON field location")
			}
			if err := json.Unmarshal([]byte(line[at+1:]), pair.target); err != nil {
				return nil, err
			}
		}
		if lineInput != input || lineTitle != r.Title {
			return nil, fmt.Errorf("original line mismatch")
		}
		for key, value := range rows[r.Index].Fields {
			labels[key] = append(json.RawMessage(nil), value...)
		}
		set("next_scout_a_original_fields", rows[r.Index].Fields)
	} else {
		if !strings.Contains(lines[r.InputLine-1], input) || !strings.Contains(lines[r.TitleLine-1], r.Title) {
			return nil, fmt.Errorf("original assertion location mismatch")
		}
		// Preserve EVERY assertion block containing the input, including later quality
		// assertions for an earlier title case. The tuple loop itself is retained too.
		blocks := []string{}
		starts := regexp.MustCompile(`(?m)^test\(`).FindAllIndex(raw, -1)
		for i, start := range starts {
			end := len(raw)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			block := string(raw[start[0]:end])
			if strings.Contains(block, "'"+input+"'") {
				blocks = append(blocks, block)
			}
		}
		if len(blocks) == 0 {
			return nil, fmt.Errorf("original assertion missing")
		}
		if r.Block != "" && !strings.Contains(string(raw), r.Block) {
			return nil, fmt.Errorf("literal assertion block changed")
		}
		set("next_scout_a_original_blocks", blocks)
		native := map[string]json.RawMessage{}
		if r.Index >= 10000 {
			native["title"], _ = json.Marshal(r.Title)
			native["group"], _ = json.Marshal(r.Group)
		} else {
			// Only direct literal equalities are decoded. The untouched source expressions
			// (including externalId object and full arrays) are retained separately.
			expressions := map[string][]string{}
			call := regexp.MustCompile(`assert\.(?:equal|deepEqual)\((?:got|result\.parsed)\.([A-Za-z.]+),\s*([^\n]+)`)
			for _, block := range blocks {
				for _, m := range call.FindAllStringSubmatch(block, -1) {
					expr, err := nextScoutALiteralArgument(m[2])
					if err != nil {
						return nil, err
					}
					expressions[m[1]] = append(expressions[m[1]], expr)
					value, err := nextScoutALiteralJSON(expr)
					if err != nil {
						return nil, err
					}
					if old, ok := native[m[1]]; ok && !bytes.Equal(old, value) {
						return nil, fmt.Errorf("conflicting original %s assertions", m[1])
					}
					native[m[1]] = value
				}
			}
			set("next_scout_a_original_expressions", expressions)
		}
		var title string
		if err := json.Unmarshal(native["title"], &title); err != nil || title != r.Title {
			return nil, fmt.Errorf("independent title equality missing")
		}
		for key, value := range native {
			labels["next_scout_a_native_"+key] = value
		}
		for from, to := range map[string]string{"title": "title", "year": "year", "group": "group", "seasonNumber": "season", "episodeNumbers": "episode"} {
			if value, ok := native[from]; ok {
				labels[to] = value
			}
		}
		if value, ok := native["kind"]; ok {
			var kind string
			_ = json.Unmarshal(value, &kind)
			if kind == "episode" || kind == "movie" {
				labels["type"] = value
			}
		}
		set("next_scout_a_original_fields", native)
	}
	return &corpusEntry{Name: input, Expected: labels}, nil
}

func nextScoutARawFieldsEqual(a, b map[string]json.RawMessage) bool {
	var av, bv any
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if json.Unmarshal(ab, &av) != nil || json.Unmarshal(bb, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
func nextScoutAResolutionToken(input, want string) bool {
	tokens := regexp.MustCompile(`[A-Za-z0-9]+`).FindAllString(input, -1)
	values := map[string]bool{}
	for _, m := range tokens {
		token := strings.ToLower(m)
		if !slices.Contains([]string{"2160p", "1080p", "720p", "480p", "4k", "uhd", "fhd", "2160i", "1080i", "720i", "480i"}, token) {
			continue
		}
		switch token {
		case "4k", "uhd":
			token = "2160p"
		case "fhd":
			token = "1080p"
		}
		if strings.HasSuffix(token, "i") {
			return false
		}
		values[token] = true
	}
	return len(values) == 1 && values[want]
}

func nextScoutALiteralArgument(text string) (string, error) {
	depth := 0
	var quote byte
	escape := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		switch c {
		case '[', '{', '(':
			depth++
		case ']', '}':
			depth--
		case ')':
			if depth == 0 {
				return strings.TrimSpace(text[:i]), nil
			}
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(text[:i]), nil
			}
		}
	}
	return "", fmt.Errorf("unterminated original assertion argument")
}
func nextScoutALiteralJSON(expr string) (json.RawMessage, error) {
	if strings.HasPrefix(expr, "'") {
		if len(expr) < 2 || expr[len(expr)-1] != '\'' || strings.Contains(expr[1:len(expr)-1], "\\") {
			return nil, fmt.Errorf("unsupported escaped source literal")
		}
		return json.Marshal(expr[1 : len(expr)-1])
	}
	if expr == "null" || expr == "true" || expr == "false" || json.Valid([]byte(expr)) {
		return json.RawMessage(expr), nil
	}
	// Keep the decoded original types alongside untouched source expressions.
	switch expr {
	case "{ source: 'tmdb', id: '603' }":
		return json.RawMessage(`{"source":"tmdb","id":"603"}`), nil
	case "['Interstellar (2014)']":
		return json.RawMessage(`["Interstellar (2014)"]`), nil
	case "['WIDESCREEN']":
		return json.RawMessage(`["WIDESCREEN"]`), nil
	}
	return nil, fmt.Errorf("nonliteral independent source expression %q", expr)
}

// Refutations supplement the unchanged strict checker; unknown attributes may
// remain unknown, but unsupported facts and partial numbering cannot be promoted.
func validateNextScoutAOriginalRebuttals(original *corpusEntry, raw json.RawMessage) error {
	if original == nil {
		return fmt.Errorf("missing next-A original")
	}
	var w struct {
		Year       int      `json:"year"`
		Season     int      `json:"season"`
		Episodes   []int    `json:"episodes"`
		Keys       []string `json:"unit_keys"`
		Absolute   int      `json:"absolute_episode"`
		Anime      bool     `json:"is_anime"`
		Confidence float64  `json:"confidence"`
		Review     bool     `json:"requires_review"`
		Quality    struct {
			Resolution string `json:"resolution"`
			Source     string `json:"source"`
			Codec      string `json:"codec"`
			HDR        string `json:"hdr"`
			Audio      string `json:"audio"`
			Edition    string `json:"edition"`
			Group      string `json:"group"`
		} `json:"quality"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	var native map[string]json.RawMessage
	if err := json.Unmarshal(original.Expected["next_scout_a_original_fields"], &native); err != nil {
		return err
	}
	get := func(key string) string { var s string; _ = json.Unmarshal(native[key], &s); return s }
	if w.Quality.Audio != "" || w.Quality.Edition != "" {
		return fmt.Errorf("unmeasured audio or edition promotion")
	}
	q := w.Quality
	if q.Source != "" {
		patterns := map[string]string{"Bluray": `(?i)(?:^|[^a-z0-9])(?:bluray|blu[ ._-]ray|bd|bdrip|brrip)(?:$|[^a-z0-9])`, "Remux": `(?i)(?:^|[^a-z0-9])(?:remux|bdremux)(?:$|[^a-z0-9])`, "WEB-DL": `(?i)(?:^|[^a-z0-9])(?:webdl|web[ ._-]dl)(?:$|[^a-z0-9])`, "WEBRip": `(?i)(?:^|[^a-z0-9])webrip(?:$|[^a-z0-9])`, "HDTV": `(?i)(?:^|[^a-z0-9])hdtv(?:$|[^a-z0-9])`}
		pattern := patterns[q.Source]
		if pattern == "" || !regexp.MustCompile(pattern).MatchString(original.Name) {
			return fmt.Errorf("source lacks complete supported token")
		}
		if regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:dvd|dvd5|dvd9|dvdrip|dvdscr|hdcam|hdts|camrip|telesync|telecine)(?:$|[^a-z0-9])`).MatchString(original.Name) {
			return fmt.Errorf("unsupported competing source declaration")
		}
		families := map[string]bool{}
		for name, pattern := range patterns {
			if regexp.MustCompile(pattern).MatchString(original.Name) {
				family := name
				if name == "Remux" {
					family = "Bluray"
				}
				families[family] = true
			}
		}
		if len(families) != 1 {
			return fmt.Errorf("conflicting source families")
		}
	}
	if q.Codec != "" && regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:xvid|divx|vp8|vc[ ._-]1)(?:$|[^a-z0-9])`).MatchString(original.Name) {
		return fmt.Errorf("unsupported competing codec declaration")
	}
	res := get("resolution")
	if res == "" {
		res = get("quality.resolution")
	}
	if q.Resolution != "" {
		allowed := map[string]string{"1080P": "1080p", "2160P": "2160p", "720P": "720p", "480P": "480p", "4KLIGHT": "2160p"}
		if raw, exists := native["resolution"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("explicit null resolution promoted")
		}
		if res != "" && allowed[strings.ToUpper(res)] != q.Resolution {
			return fmt.Errorf("original resolution contradiction")
		}
		if !nextScoutAResolutionToken(original.Name, q.Resolution) {
			return fmt.Errorf("resolution lacks complete unambiguous full-input token")
		}
	}
	src := get("quality")
	if src == "" {
		src = get("quality.source")
	}
	if q.Source != "" {
		allowed := map[string][]string{"BLURAY": {"Bluray", "Remux"}, "BDRIP": {"Bluray", "Remux"}, "REMUX": {"Remux"}, "WEB-DL": {"WEB-DL"}, "WEBRIP": {"WEBRip"}, "HDTV": {"HDTV"}}
		if src != "" && !slices.Contains(allowed[strings.ToUpper(src)], q.Source) {
			return fmt.Errorf("original unsupported/source contradiction")
		}
		if raw, ok := native["quality"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("explicit null source promoted")
		}
	}
	codec := get("codec")
	if q.Codec != "" {
		if raw, ok := native["codec"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("explicit null codec promoted")
		}
		if codec != "" && map[string]string{"H264": "H264", "AVC": "H264", "H265": "HEVC", "HEVC": "HEVC", "AV1": "AV1"}[strings.ToUpper(codec)] != q.Codec {
			return fmt.Errorf("unsupported original codec promoted")
		}
		wantToken := map[string]string{"H264": `(?i)(?:^|[^a-z0-9])(?:x264|h264|h[ ._-]264|avc)(?:$|[^a-z0-9])`, "HEVC": `(?i)(?:^|[^a-z0-9])(?:x265|h265|h[ ._-]265|hevc)(?:$|[^a-z0-9])`, "AV1": `(?i)(?:^|[^a-z0-9])av1(?:$|[^a-z0-9])`}[q.Codec]
		if wantToken == "" || !regexp.MustCompile(wantToken).MatchString(original.Name) {
			return fmt.Errorf("codec lacks complete token")
		}
	}
	if q.HDR != "" {
		pattern := map[string]string{"DV": `(?i)(?:^|[^a-z0-9])(?:dv|dovi|dolby[ ._-]vision)(?:$|[^a-z0-9])`, "HDR10": `(?i)(?:^|[^a-z0-9])(?:hdr(?:$|[^a-z0-9])|hdr10(?:$|[^a-z0-9+]))`, "HDR10+": `(?i)(?:^|[^a-z0-9])hdr10\+(?:$|[^a-z0-9])`}[q.HDR]
		if pattern == "" || !regexp.MustCompile(pattern).MatchString(original.Name) {
			return fmt.Errorf("HDR lacks complete supported token")
		}
		if raw, ok := native["v_quality"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte(`""`)) {
			return fmt.Errorf("explicit absent HDR promoted")
		}
	}
	if label, ok := native["group"]; ok && q.Group != "" {
		var group string
		if err := json.Unmarshal(label, &group); err != nil || group != q.Group {
			return fmt.Errorf("original group contradiction")
		}
	}
	zero := w.Review && w.Confidence == 0 && w.Season == 0 && len(w.Episodes) == 0 && len(w.Keys) == 0 && w.Absolute == 0 && !w.Anime
	standard := regexp.MustCompile(`(?i)\bS[0-9]{1,2}E[0-9]{1,4}\b`)
	unsafe := regexp.MustCompile(`(?i)(?:\b(?:complete|pack|ova|oav|specials?)\b|\bsaisons?[ ._-]+[0-9]+\b|\bS[0-9]{3,}E[0-9]+\b|\.part[0-9]+\.rar$|\bE[0-9]+[A-Za-z]\b)`).MatchString(original.Name)
	dated := regexp.MustCompile(`\b(?:19|20)[0-9]{2}-[0-9]{2}-[0-9]{2}\b`).MatchString(original.Name) && !standard.MatchString(original.Name)
	unbound := regexp.MustCompile(`(?i)^\[[^\]]+\].*\b[0-9]{1,3}(?:v[0-9]+)?[ ._]+(?:\[(?:480|720|1080|2160)p\][ ._]*)?\[[0-9a-f]{8}\]`).MatchString(original.Name) || (!standard.MatchString(original.Name) && regexp.MustCompile(`(?i)\bE[0-9]{1,4}\b`).MatchString(original.Name))
	if (unsafe || dated || unbound) && !zero {
		return fmt.Errorf("full-input unresolved numbering/partial declaration retained authority")
	}
	for from, key := range map[string]string{"seasonNumber": "season", "episodeNumbers": "episode"} {
		label, ok := native[from]
		if !ok {
			label, ok = original.Expected[key]
		}
		if !ok {
			continue
		}
		numbers := corpusNumbers(label)
		if len(numbers) == 0 {
			return fmt.Errorf("malformed original numbering")
		}
		if zero {
			continue
		}
		if key == "season" {
			if len(numbers) != 1 || numbers[0] != w.Season {
				return fmt.Errorf("original season mismatch")
			}
		} else if !slices.Equal(numbers, w.Episodes) {
			return fmt.Errorf("original full episode array mismatch")
		}
	}
	return nil
}

func TestNextScoutAOriginalSourceAndRefusalControls(t *testing.T) {
	var pool struct {
		Rows []nextScoutARow `json:"rows"`
	}
	raw, err := os.ReadFile(nextScoutARoot + "inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &pool); err != nil {
		t.Fatal(err)
	}
	for i, r := range pool.Rows[:63] {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			entry, err := originalNextScoutAReviewLabels(r.Source, r.Input)
			if err != nil || entry.Name != r.Input {
				t.Fatal("fixed original", err)
			}
			var title string
			if err = json.Unmarshal(entry.Expected["title"], &title); err != nil || title != r.Title {
				t.Fatal("original title", err)
			}
			if r.Source == "machard-releascenify" {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(entry.Expected["next_scout_a_original_fields"], &fields)
				if !nextScoutARawFieldsEqual(fields, r.Fields) {
					t.Fatal("native scalar/array/null dictionary rewritten")
				}
			}
		})
	}
	if _, err = originalNextScoutAReviewLabels(pool.Rows[63].Source, pool.Rows[63].Input); err == nil {
		t.Fatal("cross-partition input admitted")
	}
	if _, err = originalNextScoutAReviewLabels("unknown", pool.Rows[0].Input); err == nil {
		t.Fatal("unknown source admitted")
	}
	labels := map[string]json.RawMessage{"next_scout_a_original_fields": json.RawMessage(`{"seasonNumber":1,"episodeNumbers":[1,3],"quality.source":"DVD9","codec":"VC-1","group":null}`)}
	makeRaw := func(change map[string]any) json.RawMessage {
		w := map[string]any{"year": 0, "season": 1, "episodes": []int{1, 3}, "unit_keys": []string{"S01E01", "S01E03"}, "absolute_episode": 0, "is_anime": false, "confidence": 0.95, "requires_review": false, "quality": map[string]any{"resolution": "", "source": "", "codec": "", "hdr": "", "audio": "", "edition": "", "group": ""}}
		for k, v := range change {
			w[k] = v
		}
		b, _ := json.Marshal(w)
		return b
	}
	original := &corpusEntry{Name: "Independent S01E01E03", Expected: labels}
	if err = validateNextScoutAOriginalRebuttals(original, makeRaw(nil)); err != nil {
		t.Fatal(err)
	}
	for _, change := range []map[string]any{{"episodes": []int{1}}, {"episodes": []int{1, 2, 3}}, {"season": 2}, {"quality": map[string]string{"source": "Bluray"}}, {"quality": map[string]string{"codec": "HEVC"}}, {"quality": map[string]string{"group": "invented"}}, {"quality": map[string]string{"audio": "AAC"}}, {"quality": map[string]string{"edition": "DIRECTORS CUT"}}, {"quality": map[string]string{"resolution": "1080p"}}, {"quality": map[string]string{"hdr": "DV"}}} {
		if err = validateNextScoutAOriginalRebuttals(original, makeRaw(change)); err == nil {
			t.Fatal("full numbering/quality promotion accepted", change)
		}
	}
	for _, input := range []string{"Independent.1080p.2160p", "Independent.UHD.1080p", "Independent.1080i", "Independent.1920x1080"} {
		if nextScoutAResolutionToken(input, "1080p") {
			t.Fatal("conflicting or unmeasured resolution admitted", input)
		}
	}
	for _, input := range []string{"Independent WEB", "Independent WEB-DL DVD", "Independent WEB-DL BluRay"} {
		entry := &corpusEntry{Name: input, Expected: map[string]json.RawMessage{"next_scout_a_original_fields": json.RawMessage(`{}`)}}
		if err := validateNextScoutAOriginalRebuttals(entry, makeRaw(map[string]any{"quality": map[string]string{"source": "WEB-DL"}})); err == nil {
			t.Fatal("source declaration promoted", input)
		}
	}
	for expr, want := range map[string]string{"['Interstellar (2014)']": `["Interstellar (2014)"]`, "['WIDESCREEN']": `["WIDESCREEN"]`, "{ source: 'tmdb', id: '603' }": `{"source":"tmdb","id":"603"}`} {
		got, err := nextScoutALiteralJSON(expr)
		if err != nil || string(got) != want {
			t.Fatal("native original array/object type lost", expr, err)
		}
	}
	for _, name := range []string{"Independent COMPLETE S01E01", "Independent pack S01E01", "Independent OVA S01E01", "Independent special S01E01", "Independent Saison 2 S01E01", "Independent S2013E015", "Independent 2026-03-23", "Independent E14A", "Independent.2025.part01.rar", "Independent E07", "[Group] Independent 07 [ABCDEF12]", "[Group] Independent 07 [1080p] [ABCDEF12]"} {
		entry := &corpusEntry{Name: name, Expected: labels}
		if err = validateNextScoutAOriginalRebuttals(entry, makeRaw(nil)); err == nil {
			t.Fatal("unsafe scope promoted", name)
		}
		if err = validateNextScoutAOriginalRebuttals(entry, makeRaw(map[string]any{"season": 0, "episodes": []int{}, "unit_keys": []string{}, "confidence": 0, "requires_review": true})); err != nil {
			t.Fatal("safe refusal rejected", name, err)
		}
	}
}
