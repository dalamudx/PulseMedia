package media_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func decodePTTMainPool(raw []byte) (pttLiteralPool, error) {
	var p pttLiteralPool
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if p.Schema != "pulse.python-literal-fixtures.v1" || p.Source != "ptt-main-original-assertions" || p.License != "MIT" || p.Status != "unreviewed" || p.Accepted == nil || *p.Accepted || p.Authority == nil || *p.Authority || !strings.Contains(p.Scope, "not full coverage") || p.Count != len(p.Entries) {
		return p, fmt.Errorf("pending main dictionary cannot gain authority")
	}
	return p, nil
}
func originalPTTMainReviewLabels(p pttLiteralPool, name string) (*corpusEntry, error) {
	labels := map[string]json.RawMessage{}
	found := false
	for _, r := range p.Entries {
		if r.Name != name {
			continue
		}
		if found {
			return nil, fmt.Errorf("duplicate main input")
		}
		found = true
		if len(r.Observations) == 0 {
			return nil, fmt.Errorf("missing original dictionaries")
		}
		for _, o := range r.Observations {
			if o.Path != "tests/test_main.py" || o.Function != "test_random_releases_parse" || o.Line < 15 || len(o.Fields) != 1 {
				return nil, fmt.Errorf("missing assertion location")
			}
			raw, ok := o.Fields["expected_output"]
			if !ok {
				return nil, fmt.Errorf("missing full dictionary")
			}
			var f map[string]json.RawMessage
			if err := json.Unmarshal(raw, &f); err != nil || f == nil {
				return nil, fmt.Errorf("not a literal dictionary")
			}
			var title string
			if err := json.Unmarshal(f["title"], &title); err != nil || title == "" {
				return nil, fmt.Errorf("explicit original title required")
			}
			for k, v := range f {
				if old, ok := labels[k]; ok && !bytes.Equal(old, v) {
					return nil, fmt.Errorf("conflicting original %s", k)
				}
				labels[k] = v
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("not a pinned main input")
	}
	return mappedPTTMainReviewLabels(labels, name)
}

func mappedPTTMainReviewLabels(labels map[string]json.RawMessage, name string) (*corpusEntry, error) {
	mapped := map[string]json.RawMessage{"title": labels["title"]}
	for from, to := range map[string]string{"year": "year", "seasons": "season", "episodes": "episode", "group": "group", "resolution": "screen_size", "codec": "ptt_main_codec", "quality": "ptt_main_quality"} {
		v, ok := labels[from]
		if !ok {
			continue
		}
		if from == "episodes" || from == "seasons" {
			var a []int
			if err := json.Unmarshal(v, &a); err != nil || a == nil {
				return nil, fmt.Errorf("invalid complete %s", from)
			}
			seen := map[int]bool{}
			for _, n := range a {
				if n < 0 || (from == "episodes" && n == 0) || seen[n] {
					return nil, fmt.Errorf("invalid complete %s", from)
				}
				seen[n] = true
			}
			if from == "seasons" && len(a) == 0 {
				continue
			}
		}
		if from == "year" {
			var n int
			if err := json.Unmarshal(v, &n); err != nil || n < 1 {
				return nil, fmt.Errorf("invalid original year")
			}
		}
		if from == "group" || from == "resolution" || from == "codec" || from == "quality" {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, fmt.Errorf("invalid scalar %s", from)
			}
			// Preserve the upstream 4K label in the fixed source; its canonical
			// resolution value is 2160p, independently of the release recognizer.
			if from == "resolution" && s == "4k" {
				v = json.RawMessage(`"2160p"`)
			}
		}
		mapped[to] = v
	}
	return &corpusEntry{Name: name, Expected: mapped}, nil
}
func validatePTTMainQualityRebuttals(labels map[string]json.RawMessage, q quality.Profile) error {
	codecs := map[string]quality.Codec{"avc": quality.CodecH264, "h264": quality.CodecH264, "x264": quality.CodecH264, "hevc": quality.CodecHEVC, "h265": quality.CodecHEVC, "x265": quality.CodecHEVC, "xvid": "", "divx": "", "av1": "", "mpeg": "", "vp8": ""}
	sources := map[string][]quality.Source{"BluRay": {quality.SourceBluray}, "BluRay REMUX": {quality.SourceBluray, quality.SourceRemux}, "BDRip": {quality.SourceBluray}, "BRRip": {quality.SourceBluray}, "WEB-DL": {quality.SourceWEBDL}, "WEBRip": {quality.SourceWEBRip}, "WEB": {quality.SourceWEBDL, quality.SourceWEBRip}, "HDTV": {quality.SourceHDTV}, "DVDRip": {}, "DVD": {}, "TeleCine": {}, "HDRip": {}, "SCR": {}, "CAM": {}, "TS": {}, "TeleSync": {}}
	if v, ok := labels["ptt_main_codec"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return err
		}
		value, ok := codecs[s]
		if !ok {
			return fmt.Errorf("unaudited codec %q", s)
		}
		if q.Codec != "" && q.Codec != value {
			return fmt.Errorf("codec contradicts dictionary")
		}
	}
	if v, ok := labels["ptt_main_quality"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return err
		}
		values, ok := sources[s]
		if !ok {
			return fmt.Errorf("unaudited quality %q", s)
		}
		if q.Source != "" && !slices.Contains(values, q.Source) {
			return fmt.Errorf("source contradicts dictionary")
		}
	}
	return nil
}
func TestPinnedPTTMainDictionariesRemainPartialAndReproducible(t *testing.T) {
	root := "../../../testdata/releases/review-inputs"
	raw, err := os.ReadFile(root + "/ptt-main-original-assertions.json")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != "9474f5c969fe01961873b794e7c266c81f2a3d54000bcd2db41888c9f6294ad9" {
		t.Fatal("pinned dictionary changed")
	}
	p, err := decodePTTMainPool(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Count != 127 || len(p.Skipped) != 10 || p.Provenance.Commit != "88429bb90acef55673f421f45038878809b1e577" || p.Provenance.Repository != "dreulavelle/PTT" {
		t.Fatal("origin/count changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "node", "-e", "const {build}=require('./scripts/extract_ptt_main_review_inputs.cjs');process.stdout.write(JSON.stringify(build('testdata/releases/review-inputs/ptt-original-assertions.sources'),null,2)+'\\n')")
	c.Dir = "../../.."
	out, err := c.CombinedOutput()
	if err != nil || !bytes.Equal(out, raw) {
		t.Fatalf("original reproduction mismatch: %v %s", err, out)
	}
	seen := map[string]bool{}
	for _, r := range p.Entries {
		if seen[r.Name] {
			t.Fatal("duplicate input")
		}
		seen[r.Name] = true
		if _, err := originalPTTMainReviewLabels(p, r.Name); err != nil {
			t.Fatal(r.Name, err)
		}
	}
	for _, flag := range []string{"formal_golden_acceptance", "coverage_authority"} {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		obj[flag] = true
		b, _ := json.Marshal(obj)
		if _, err := decodePTTMainPool(b); err == nil {
			t.Fatal("pool gained", flag)
		}
	}
}
func TestPTTMainAdmissionPreservesFullArraysAndRejectsConflicts(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/releases/review-inputs/ptt-main-original-assertions.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := decodePTTMainPool(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.Entries = p.Entries[:1]
	r := &p.Entries[0]
	r.Name = "Probe.S02E09E10"
	r.Observations = r.Observations[:1]
	o := &r.Observations[0]
	o.Fields = map[string]json.RawMessage{"expected_output": json.RawMessage(`{"title":"Probe","seasons":[2],"episodes":[9,10],"codec":"hevc","quality":"WEB-DL"}`)}
	orig, err := originalPTTMainReviewLabels(p, r.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig.Expected["episode"], []byte("[9,10]")) {
		t.Fatal("array reduced to firstgetter")
	}
	if err := validatePTTMainQualityRebuttals(orig.Expected, quality.Profile{Source: quality.SourceBluray, Codec: quality.CodecH264}); err == nil {
		t.Fatal("contradictory source/codec accepted")
	}
	for _, bad := range []string{`{"title":"Probe","episodes":[9,9]}`, `{"title":"Probe","episodes":[0]}`, `{"title":"Probe","year":0}`, `{"title":null}`, `{"title":"Probe","codec":["hevc"]}`} {
		o.Fields["expected_output"] = json.RawMessage(bad)
		if _, err := originalPTTMainReviewLabels(p, r.Name); err == nil {
			t.Fatal("invalid dictionary admitted", bad)
		}
	}
	o.Fields = map[string]json.RawMessage{"expected_output": json.RawMessage(`{"title":"Probe"}`)}
	o.Line = 0
	if _, err := originalPTTMainReviewLabels(p, r.Name); err == nil {
		t.Fatal("missing location accepted")
	}
	o.Line = 15
	r.Observations = append(r.Observations, *o)
	r.Observations[1].Fields = map[string]json.RawMessage{"expected_output": json.RawMessage(`{"title":"Other"}`)}
	if _, err := originalPTTMainReviewLabels(p, r.Name); err == nil {
		t.Fatal("conflicting titles accepted")
	}
}

func TestPTTMain4KResolutionAliasPreservesIndependentEvidence(t *testing.T) {
	labels := map[string]json.RawMessage{"title": json.RawMessage(`"Movie"`), "resolution": json.RawMessage(`"4k"`)}
	entry, err := mappedPTTMainReviewLabels(labels, "Movie.2160p")
	if err != nil {
		t.Fatal(err)
	}
	if string(labels["resolution"]) != `"4k"` || string(entry.Expected["screen_size"]) != `"2160p"` {
		t.Fatal("original or canonical resolution changed")
	}
	if err := validateReviewedQualityLabels(entry.Expected, quality.Profile{Resolution: quality.Res2160p}); err != nil {
		t.Fatal(err)
	}
	if err := validateReviewedQualityLabels(entry.Expected, quality.Profile{Resolution: quality.Res1080p}); err == nil {
		t.Fatal("4K evidence admitted 1080p")
	}
}
