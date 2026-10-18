package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"slices"
	"strconv"
	"testing"
)

const (
	atamaOriginalCommit        = "d3d730399aa83805140b88bdcacdb3b8eb3216be"
	atamaOriginalJSONSHA256    = "577e0e718d496781ae94183bd9b60a21505b0d5e67c560e98c431ac33ff4a83c"
	atamaOriginalPHPSHA256     = "963d02bd058f469b23b12c1e0596f57af7fb0033ff9312910ea755bed280ca87"
	atamaOriginalLicenseSHA256 = "da1ab0a4449807fd161b8d028f79c42cc00e2e67ee8f9f583cb37a2c2cbaa0fd"
)

// This is an inert source adapter, not an upstream runner or a gold generator.
// The PHP provider skips should_fail and removes comment; its top-level
// assertions are partial. Omitted labels are not independently negative facts.
func atamaOriginalIndex() (map[string]map[string]json.RawMessage, error) {
	root := "../../../testdata/releases/"
	manifest, err := os.ReadFile(root + "review-inputs/atama-metadata-original.manifest.json")
	if err != nil {
		return nil, err
	}
	var m struct {
		ID        string `json:"source_id"`
		License   string `json:"license"`
		Status    string `json:"review_status"`
		Reviewed  *int   `json:"reviewed_entries"`
		Count     int    `json:"candidate_count"`
		Accepted  *bool  `json:"accepted"`
		Gold      *bool  `json:"formal_golden_acceptance"`
		Canonical *bool  `json:"canonical_unit_authorization"`
		Coverage  *bool  `json:"coverage_authority"`
		Counts    struct {
			Raw      int `json:"raw_keys"`
			Skipped  int `json:"should_fail_skipped"`
			Enabled  int `json:"provider_enabled_entries"`
			Nameless int `json:"enabled_without_name_not_admissible"`
			Named    int `json:"enabled_with_name"`
		} `json:"counts"`
		Excluded struct {
			Skipped  []string `json:"should_fail"`
			Nameless []string `json:"enabled_without_name"`
		} `json:"excluded_inputs"`
		Provenance struct {
			Repository string `json:"repository"`
			Commit     string `json:"commit"`
			Files      []struct {
				Path   string `json:"path"`
				URL    string `json:"url"`
				Source string `json:"source_file"`
				Hash   string `json:"sha256"`
				Bytes  int    `json:"bytes"`
			} `json:"files"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, err
	}
	for _, flag := range []*bool{m.Accepted, m.Gold, m.Canonical, m.Coverage} {
		if flag == nil || *flag {
			return nil, fmt.Errorf("Atama pending source cannot gain authority")
		}
	}
	if m.ID != "atama-metadata-original" || m.License != "MIT" || m.Status != "partially_reviewed_literal_source" || m.Reviewed == nil || *m.Reviewed != 37 || m.Count != 39 || m.Provenance.Repository != "https://github.com/odangomoe/Atama" || m.Provenance.Commit != atamaOriginalCommit {
		return nil, fmt.Errorf("Atama provenance or source-wide review status changed")
	}
	files := []struct {
		path, source, hash string
		size               int
	}{
		{"test/data/metadata-title.json", "../upstream-mit/atama-metadata-title-original.json", atamaOriginalJSONSHA256, 10816},
		{"test/MetadataTest.php", "../upstream-mit/atama-metadata-original.test.php", atamaOriginalPHPSHA256, 2685},
		{"LICENSE", "atama-metadata-original.LICENSE", atamaOriginalLicenseSHA256, 1067},
	}
	if len(m.Provenance.Files) != len(files) {
		return nil, fmt.Errorf("Atama file inventory changed")
	}
	var original []byte
	for i, file := range files {
		got := m.Provenance.Files[i]
		if got.Path != file.path || got.Source != file.source || got.Hash != file.hash || got.Bytes != file.size || got.URL != "https://raw.githubusercontent.com/odangomoe/Atama/"+atamaOriginalCommit+"/"+file.path {
			return nil, fmt.Errorf("Atama immutable file declaration changed: %s", file.path)
		}
		raw, err := os.ReadFile(root + "review-inputs/" + file.source)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(raw)
		if len(raw) != file.size || hex.EncodeToString(h[:]) != file.hash {
			return nil, fmt.Errorf("Atama fixed bytes changed: %s", file.path)
		}
		if i == 0 {
			original = raw
		}
	}
	var index map[string]map[string]json.RawMessage
	if err := json.Unmarshal(original, &index); err != nil {
		return nil, err
	}
	var skipped, nameless []string
	named := 0
	for input, labels := range index {
		var skip bool
		if v, exists := labels["should_fail"]; exists {
			if err := json.Unmarshal(v, &skip); err != nil {
				return nil, err
			}
		}
		if skip {
			skipped = append(skipped, input)
			continue
		}
		var name string
		if v, exists := labels["name"]; exists {
			if err := json.Unmarshal(v, &name); err != nil || name == "" {
				return nil, fmt.Errorf("invalid literal Atama name")
			}
		} else {
			nameless = append(nameless, input)
			continue
		}
		named++
	}
	slices.Sort(skipped)
	slices.Sort(nameless)
	slices.Sort(m.Excluded.Skipped)
	slices.Sort(m.Excluded.Nameless)
	if len(index) != 44 || len(skipped) != 3 || len(nameless) != 2 || named != 39 || m.Counts.Raw != len(index) || m.Counts.Skipped != len(skipped) || m.Counts.Enabled != len(index)-len(skipped) || m.Counts.Nameless != len(nameless) || m.Counts.Named != named || !slices.Equal(skipped, m.Excluded.Skipped) || !slices.Equal(nameless, m.Excluded.Nameless) {
		return nil, fmt.Errorf("Atama actual raw/skipped/nameless/named inventory differs")
	}
	return index, nil
}

// Decimal JSON numbers such as 7.0 remain untouched in source evidence. Only
// positive exact integers get a separate compatible view; suffixes never do.
func atamaIntegralEpisodes(raw json.RawMessage) ([]int, bool) {
	var values []json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	if bytes.TrimSpace(raw)[0] == '[' {
		if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
			return nil, false
		}
	} else {
		values = []json.RawMessage{raw}
	}
	out := make([]int, 0, len(values))
	for _, value := range values {
		r, ok := new(big.Rat).SetString(string(value))
		if !ok || !r.IsInt() || !r.Num().IsInt64() || r.Sign() <= 0 {
			return nil, false
		}
		n := r.Num().Int64()
		if int64(int(n)) != n || slices.Contains(out, int(n)) {
			return nil, false
		}
		out = append(out, int(n))
	}
	return out, true
}

func originalAtamaReviewLabels(input string) (*corpusEntry, error) {
	index, err := atamaOriginalIndex()
	if err != nil {
		return nil, err
	}
	labels, exists := index[input]
	if !exists {
		return nil, fmt.Errorf("not a fixed original Atama input")
	}
	var skip bool
	if v, exists := labels["should_fail"]; exists {
		if err := json.Unmarshal(v, &skip); err != nil {
			return nil, err
		}
	}
	var name string
	if skip || json.Unmarshal(labels["name"], &name) != nil || name == "" {
		return nil, fmt.Errorf("Atama skipped or nameless record is not admissible")
	}
	view := map[string]json.RawMessage{"title": labels["name"]}
	for from, to := range map[string]string{"group": "group", "resolution": "resolution", "video": "codec", "source": "quality"} {
		if value, exists := labels[from]; exists {
			view[to] = value
		}
	}
	// Canonicalize only the assertion view; atama_original_* below keeps every
	// original byte and spelling. Unknown source labels cannot be admitted.
	if value, ok := labels["source"]; ok {
		var literal string
		if err := json.Unmarshal(value, &literal); err != nil {
			return nil, err
		}
		known := map[string]string{"bd": "BluRay", "blu-ray": "BluRay", "webrip": "WEBRip", "web": "WEB", "dvd-rip": "DVDRip", "dvd-iso": "DVDRip"}
		canonical, ok := known[literal]
		if !ok {
			return nil, fmt.Errorf("unaudited original Atama source %q", literal)
		}
		view["quality"], err = json.Marshal(canonical)
		if err != nil {
			return nil, err
		}
	}
	// Original ep values have no canonical numbering scheme. Only a matching
	// literal raw SxxExx plus original season and one episode gets that view.
	if value, exists := labels["season"]; exists {
		var season int
		if json.Unmarshal(value, &season) != nil || season < 0 {
			return nil, fmt.Errorf("invalid Atama source season")
		}
		episodes, integral := atamaIntegralEpisodes(labels["ep"])
		matches := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])S([0-9]{1,2})E([0-9]{1,4})(?:$|[^A-Za-z0-9])`).FindAllStringSubmatch(input, -1)
		if integral && len(episodes) == 1 && len(matches) == 1 {
			s, se := strconv.Atoi(matches[0][1])
			e, ee := strconv.Atoi(matches[0][2])
			if se == nil && ee == nil && s == season && e == episodes[0] {
				view["seasons"], err = json.Marshal([]int{season})
				if err != nil {
					return nil, err
				}
				view["episodes"], err = json.Marshal(episodes)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	entry, err := mappedPTTMainReviewLabels(view, input)
	if err != nil {
		return nil, err
	}
	// Preserve ALL original fields, including null, comments, version, 1.0,
	// mixed [1.0,"A"], and nested collection ENDPOINTS without expansion.
	for key, value := range labels {
		entry.Expected["atama_original_"+key] = bytes.Clone(value)
	}
	entry.Expected["atama_original_fields"], err = json.Marshal(labels)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// raw is the reviewed row's Expected JSON, matching the existing review helpers.
// This only rebuts unsupported coverage; it never constructs a gold annotation.
func validateAtamaCoverageRefusal(original *corpusEntry, raw json.RawMessage) error {
	if original == nil {
		return fmt.Errorf("missing original Atama evidence")
	}
	var labels map[string]json.RawMessage
	if json.Unmarshal(original.Expected["atama_original_fields"], &labels) != nil || labels == nil {
		return fmt.Errorf("missing complete Atama source fields")
	}
	var kind string
	if v, exists := labels["type"]; exists {
		if json.Unmarshal(v, &kind) != nil {
			return fmt.Errorf("invalid source type")
		}
	}
	blocked := false
	switch kind {
	case "batch", "season", "collection", "special", "volume", "unknown":
		blocked = true
	case "", "ep":
	default:
		return fmt.Errorf("unaudited Atama source type %q", kind)
	}
	if _, exists := labels["collection"]; exists {
		blocked = true
	}
	// A volume label also blocks coverage when upstream omitted type.
	if _, exists := labels["volume"]; exists {
		blocked = true
	}
	episodes, integral := atamaIntegralEpisodes(labels["ep"])
	_, hasEpisode := labels["ep"]
	if hasEpisode && !integral {
		blocked = true
	}
	var w struct {
		Review     bool     `json:"requires_review"`
		Confidence float64  `json:"confidence"`
		Keys       []string `json:"unit_keys"`
		Season     int      `json:"season"`
		Episodes   []int    `json:"episodes"`
		Absolute   int      `json:"absolute_episode"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	// Only a SINGLE explicit standard raw SxxExx, corroborated by the source
	// season and episode, can escape refusal for episode-bearing records.
	explicit := false
	if !blocked && hasEpisode && integral && len(episodes) == 1 {
		var season int
		if value, exists := labels["season"]; exists && json.Unmarshal(value, &season) == nil && season >= 0 {
			matches := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])S([0-9]{1,2})E([0-9]{1,4})(?:$|[^A-Za-z0-9])`).FindAllStringSubmatch(original.Name, -1)
			if len(matches) == 1 {
				s, e1 := strconv.Atoi(matches[0][1])
				e, e2 := strconv.Atoi(matches[0][2])
				explicit = e1 == nil && e2 == nil && s == season && e == episodes[0]
			}
			if explicit && (w.Season != season || !slices.Equal(w.Episodes, episodes) || len(w.Keys) != 1 || w.Keys[0] != fmt.Sprintf("S%02dE%02d", season, episodes[0]) || w.Absolute != 0) {
				return fmt.Errorf("reviewed standard coverage contradicts literal Atama season/episode")
			}
		}
	}
	if blocked || (hasEpisode && !explicit) {
		if !w.Review || w.Confidence != 0 || w.Season != 0 || len(w.Episodes) != 0 || len(w.Keys) != 0 {
			return fmt.Errorf("Atama partial/ambiguous coverage cannot certify units or infer a default season")
		}
		if w.Absolute != 0 && (blocked || len(episodes) != 1 || w.Absolute != episodes[0]) {
			return fmt.Errorf("Atama absolute hint must match one original integral episode and requires a current mapping")
		}
	}
	return nil
}

func TestAtamaOriginalInventoryAndEvidence(t *testing.T) {
	index, err := atamaOriginalIndex()
	if err != nil {
		t.Fatal(err)
	}
	for input, labels := range index {
		entry, err := originalAtamaReviewLabels(input)
		_, named := labels["name"]
		if bytes.Equal(labels["should_fail"], []byte("true")) || !named {
			if err == nil {
				t.Fatalf("excluded source admitted: %s", input)
			}
			continue
		}
		if err != nil {
			t.Fatal(input, err)
		}
		for key, value := range labels {
			if !bytes.Equal(entry.Expected["atama_original_"+key], value) {
				t.Fatalf("original field changed: %s / %s", input, key)
			}
		}
	}
}

// Eight independently read literal-source audits, NOT complete reviewed gold.
// No absent quality/year/identity field is supplied from tokens in the input.
// Intended complete contracts still need individual title, media type, full
// quality, coverage, confidence and reason review plus Main's registration.
func TestAtamaEightLiteralSourceAudits(t *testing.T) {
	for _, audit := range []struct{ input, title, field, value string }{
		{"Kabaneri of the Iron Fortress (1080p)", "Kabaneri of the Iron Fortress", "resolution", `"1080p"`},
		{"[DeadFish] Psycho-Pass 2 - 11 [720p][AAC].mp4", "Psycho-Pass 2", "ep", `[11.0]`},
		{"[Cthune] Rolling Girls - 09 [720p H264 AAC][EBDE8F5B].mkv", "Rolling Girls", "video", `"h264"`},
		{"[Coalgirls]_Nisekoi_01_(1920x1080_Blu-ray_FLAC)_[F68B03CD].mkv", "Nisekoi", "source", `"blu-ray"`},
		{"[SallySubs] Log Horizon 2 [BD 1080p FLAC]", "Log Horizon 2", "audio", `"flac"`},
		{"[Licca] Little Women II - Jo's Boys Episode 4 (VP8)", "Little Women II - Jo's Boys", "video", `"vp8"`},
		{"[inid4c] JoJo's Bizarre Adventure - Stone Ocean 38 v2 (1080p)", "JoJo's Bizarre Adventure - Stone Ocean", "version", `2`},
		{"[EMBER] Synduality: Noir S01E02 [1080p] [HEVC WEBRip]", "Synduality: Noir", "ep", `[2.0]`},
	} {
		entry, err := originalAtamaReviewLabels(audit.input)
		if err != nil {
			t.Fatal(err)
		}
		var title string
		if json.Unmarshal(entry.Expected["title"], &title) != nil || title != audit.title || !bytes.Equal(entry.Expected["atama_original_"+audit.field], []byte(audit.value)) {
			t.Fatal("literal audit differs", audit.input)
		}
	}
}

func TestAtamaCoverageEvidenceCannotAuthorizePartialUnits(t *testing.T) {
	index, err := atamaOriginalIndex()
	if err != nil {
		t.Fatal(err)
	}
	for input := range index {
		entry, err := originalAtamaReviewLabels(input)
		if err != nil {
			continue
		} // The inventory test checks all exclusions.
		var labels map[string]json.RawMessage
		if err := json.Unmarshal(entry.Expected["atama_original_fields"], &labels); err != nil {
			t.Fatal(err)
		}
		_, ep := labels["ep"]
		_, collection := labels["collection"]
		_, volume := labels["volume"]
		_, kind := labels["type"]
		_, season := labels["season"]
		if season && ep {
			continue
		} // Explicit corroborated SxxExx is tested below.
		if !ep && !collection && !volume && !kind {
			continue
		}
		if err := validateAtamaCoverageRefusal(entry, json.RawMessage(`{"requires_review":true,"confidence":0,"season":0,"episodes":[],"unit_keys":[],"absolute_episode":0}`)); err != nil {
			t.Fatal(input, err)
		}
		for _, body := range []string{
			`{"requires_review":false}`, `{"requires_review":true,"confidence":0.9}`,
			`{"requires_review":true,"season":1}`, `{"requires_review":true,"episodes":[1]}`,
			`{"requires_review":true,"unit_keys":["movie"]}`, `{"requires_review":true,"absolute_episode":1000000}`,
		} {
			if validateAtamaCoverageRefusal(entry, json.RawMessage(body)) == nil {
				t.Fatal("partial coverage admitted", input, body)
			}
		}
	}
	entry, err := originalAtamaReviewLabels("[EMBER] Synduality: Noir S01E02 [1080p] [HEVC WEBRip]")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAtamaCoverageRefusal(entry, json.RawMessage(`{"season":1,"episodes":[2],"unit_keys":["S01E02"],"absolute_episode":0}`)); err != nil {
		t.Fatal(err)
	}
	if validateAtamaCoverageRefusal(entry, json.RawMessage(`{"season":1,"episodes":[3],"unit_keys":["S01E03"]}`)) == nil {
		t.Fatal("mismatched explicit coverage admitted")
	}
	// The fractional record lacks name and stays excluded; its literal shape
	// must nevertheless never be truncated by the numeric-view helper.
	for _, value := range []string{`[12.5]`, `[1.0,"A"]`, `[]`, `null`, `[0]`} {
		if _, ok := atamaIntegralEpisodes(json.RawMessage(value)); ok {
			t.Fatal("nonintegral episode view admitted", value)
		}
	}
}

func TestAtamaNumericViewPreservesOriginalDecimalBytes(t *testing.T) {
	raw := json.RawMessage(`[4.0,7.0]`)
	before := bytes.Clone(raw)
	got, ok := atamaIntegralEpisodes(raw)
	if !ok || !slices.Equal(got, []int{4, 7}) || !bytes.Equal(raw, before) {
		t.Fatalf("numeric view mutated fixed original bytes: %s / %v", raw, got)
	}
	entry, err := originalAtamaReviewLabels("[Cthune] Rolling Girls - 09 [720p H264 AAC][EBDE8F5B].mkv")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAtamaCoverageRefusal(entry, json.RawMessage(`{"requires_review":true,"confidence":0,"season":0,"episodes":[],"unit_keys":[],"absolute_episode":9}`)); err != nil {
		t.Fatal(err)
	}
	if validateAtamaCoverageRefusal(entry, json.RawMessage(`{"requires_review":true,"confidence":0,"absolute_episode":8}`)) == nil {
		t.Fatal("substituted absolute hint admitted")
	}
}
