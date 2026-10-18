package media_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"os"
	"reflect"
	"testing"
)

func TestIndependentSafetyAnnotations(t *testing.T) {
	file, e := os.Open("../../../testdata/releases/safety.jsonl")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	ids, inputs := map[string]bool{}, map[string]bool{}
	count := 0
	for scanner.Scan() {
		var c struct {
			ID     string   `json:"id"`
			Input  string   `json:"input"`
			Keys   []string `json:"keys"`
			Review bool     `json:"review"`
			Reason string   `json:"reason"`
		}
		d := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		d.DisallowUnknownFields()
		if e = d.Decode(&c); e != nil {
			t.Fatal(e)
		}
		if c.ID == "" || c.Input == "" || c.Reason == "" || ids[c.ID] || inputs[c.Input] {
			t.Fatal("invalid/duplicate annotation", c.ID)
		}
		ids[c.ID], inputs[c.Input] = true, true
		count++
		r := media.RecognizeRelease(c.Input)
		if !reflect.DeepEqual(r.UnitKeys, c.Keys) || r.RequiresReview != c.Review || r.Reason != c.Reason {
			t.Errorf("annotation %s wrong coverage/review: %+v", c.ID, r)
		}
	}
	if e = scanner.Err(); e != nil {
		t.Fatal(e)
	}
	if count != 20 {
		t.Fatalf("expected current reviewed safety set of 20, not synthetic combination count: %d", count)
	}
}
