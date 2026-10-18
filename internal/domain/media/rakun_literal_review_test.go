package media_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var rakunLiteralBlock = regexp.MustCompile(`(?ms)^\s*\{\s*\n(.*?)^\s*\},?\s*$`)
var rakunLiteralField = regexp.MustCompile(`^([a-z_]+):\s*("(?:\\.|[^"\\])*")\s*,?$`)
var rakunScalarNumber = regexp.MustCompile(`^[0-9]{1,4}$`)

func decodeRakunLiteralFields(body string) (map[string]string, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := rakunLiteralField.FindStringSubmatch(line)
		if len(m) != 3 {
			return nil, fmt.Errorf("not a literal string declaration")
		}
		if _, exists := fields[m[1]]; exists {
			return nil, fmt.Errorf("duplicate original field")
		}
		var value string
		if err := json.Unmarshal([]byte(m[2]), &value); err != nil || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("invalid original literal")
		}
		fields[m[1]] = value
	}
	if fields["filename"] == "" || fields["name"] == "" {
		return nil, fmt.Errorf("missing original input or title")
	}
	return fields, nil
}

func mappedRakunLiteralLabels(fields map[string]string) (*corpusEntry, error) {
	labels := map[string]json.RawMessage{}
	put := func(key string, value any) { labels[key], _ = json.Marshal(value) }
	put("title", fields["name"])
	put("original_rakun_fields", fields)
	for _, key := range []string{"season", "episode", "part", "movie"} {
		value, exists := fields[key]
		if !exists {
			continue
		}
		if key == "episode" && validRakunEpisodeRange(value) {
			put("rakun_episode_range", value)
			put("type", "episode")
			continue
		}
		if key == "movie" || !rakunScalarNumber.MatchString(value) {
			return nil, fmt.Errorf("unreviewed numbering declaration")
		}
		number, err := strconv.Atoi(value)
		if err != nil || (key != "season" && number == 0) {
			return nil, fmt.Errorf("invalid original numbering")
		}
		put("type", "episode")
		if key == "part" {
			put("rakun_part", number)
		} else {
			put(key, number)
		}
	}
	if value, exists := fields["resolution"]; exists {
		put("screen_size", value)
	}
	if value, exists := fields["source"]; exists {
		switch value {
		case "bluray":
			put("source", "Blu-ray")
		case "raw":
			put("source", "Raw")
		case "dvd":
			put("source", "DVD")
		default:
			return nil, fmt.Errorf("unreviewed original source label")
		}
	}
	codec := ""
	for _, token := range strings.Fields(fields["codecs"]) {
		next := ""
		switch token {
		case "x264", "h264", "avc":
			next = "H.264"
		case "x265", "hevc":
			next = "H.265"
		case "aac", "ac3", "eac3", "flac", "10bit", "8bit", "dual_audio", "audio_5_1", "yuv444p10", "true_hd":
		default:
			return nil, fmt.Errorf("unreviewed original codec label")
		}
		if next == "" {
			continue
		}
		if codec != "" && codec != next {
			return nil, fmt.Errorf("conflicting original video codecs")
		}
		codec = next
	}
	if codec != "" {
		put("video_codec", codec)
	}
	return &corpusEntry{Name: fields["filename"], Expected: labels}, nil
}

func originalRakunReviewLabels(input string) (*corpusEntry, error) {
	root := "../../../testdata/releases/"
	var fixture []byte
	for _, snap := range []struct{ path, sum string }{
		{"upstream-mit/rakun-full-original.test.js", "888c44b6f29deeb52fcf2ced59873326973881aae4976c725c38d258ba33f91c"},
		{"upstream-mit/rakun-full-original.tester.js", "56a8bcd6273986354ffc61d0d933d8567ac785737fceaa4268c20113682ed569"},
		{"review-inputs/rakun-full-original.LICENSE", "d14c13422112e483e91faf496cc0bfd75ec072766053a340b2ff9afc483fb468"},
	} {
		raw, err := os.ReadFile(root + snap.path)
		if err != nil {
			return nil, err
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != snap.sum {
			return nil, fmt.Errorf("fixed original source or license changed")
		}
		if strings.HasSuffix(snap.path, ".test.js") {
			fixture = raw
		}
	}
	var found *corpusEntry
	for _, block := range rakunLiteralBlock.FindAllSubmatch(fixture, -1) {
		fields, err := decodeRakunLiteralFields(string(block[1]))
		if err != nil {
			return nil, err
		}
		if fields["filename"] != input {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("duplicate original input")
		}
		found, err = mappedRakunLiteralLabels(fields)
		if err != nil {
			return nil, err
		}
	}
	if found == nil {
		return nil, fmt.Errorf("not a fixed literal source input")
	}
	return found, nil
}

func validateRakunPartRefusal(original *corpusEntry, raw json.RawMessage) error {
	if _, exists := original.Expected["rakun_part"]; !exists {
		return nil
	}
	var w struct {
		Review     bool     `json:"requires_review"`
		Confidence float64  `json:"confidence"`
		Keys       []string `json:"unit_keys"`
		Episodes   []int    `json:"episodes"`
		Season     int      `json:"season"`
		Absolute   int      `json:"absolute_episode"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	if !w.Review || w.Confidence != 0 || w.Season != 0 || w.Absolute != 0 || len(w.Keys) != 0 || len(w.Episodes) != 0 {
		return fmt.Errorf("part-relative numbering cannot certify canonical coverage")
	}
	return nil
}

func TestRakunLiteralAdmissionRejectsComputedAndPartialEvidence(t *testing.T) {
	for _, body := range []string{
		`filename:"Input",` + "\n" + `name: title()`,
		`filename:"Input",` + "\n" + `name:""`,
		`filename:"Input",` + "\n" + `name:"Show",` + "\n" + `name:"Other"`,
		`filename: makeInput(),` + "\n" + `name:"Show"`,
	} {
		if _, err := decodeRakunLiteralFields(body); err == nil {
			t.Fatal("nonliteral or incomplete source admitted", body)
		}
	}
	for _, fields := range []map[string]string{
		{"filename": "Input", "name": "Show", "episode": "13-1"},
		{"filename": "Input", "name": "Show", "season": "1-8"},
		{"filename": "Input", "name": "Show", "episode": "17.5"},
		{"filename": "Input", "name": "Show", "episode": "0"},
		{"filename": "Input", "name": "Show", "episode": "+1"},
		{"filename": "Input", "name": "Show", "source": "raw webdl"},
		{"filename": "Input", "name": "Show", "codecs": "h264 x265"},
	} {
		if _, err := mappedRakunLiteralLabels(fields); err == nil {
			t.Fatal("unreviewed or conflicting labels admitted", fields)
		}
	}
	original, err := mappedRakunLiteralLabels(map[string]string{"filename": "Input", "name": "Show", "season": "3", "episode": "9", "part": "2", "codecs": "aac x264", "source": "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if string(original.Expected["season"]) != "3" || string(original.Expected["episode"]) != "9" || string(original.Expected["rakun_part"]) != "2" {
		t.Fatal("original numbering truncated")
	}
	for _, body := range []string{`{"requires_review":false,"confidence":0.95,"season":3,"episodes":[9],"unit_keys":["S03E09"]}`, `{"requires_review":true,"confidence":0,"absolute_episode":9}`, `{"requires_review":true,"confidence":0,"unit_keys":["S03E09"]}`} {
		if validateRakunPartRefusal(original, json.RawMessage(body)) == nil {
			t.Fatal("part-relative coverage promoted", body)
		}
	}
	if err := validateRakunPartRefusal(original, json.RawMessage(`{"requires_review":true,"confidence":0}`)); err != nil {
		t.Fatal(err)
	}
}
