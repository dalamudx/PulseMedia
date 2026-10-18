package media_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"os"
	"path/filepath"
	"testing"
)

type corpusEntry struct {
	Name     string                     `json:"release_name"`
	Expected map[string]json.RawMessage `json:"expected"`
}
type upstreamCorpus struct {
	License string        `json:"_license"`
	Entries []corpusEntry `json:"entries"`
}

func readMITCorpora(t *testing.T) []corpusEntry {
	t.Helper()
	root := "../../../testdata/releases/upstream-mit"
	raw, e := os.ReadFile(filepath.Join(root, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	var manifest struct {
		Unique   int  `json:"unique_inputs"`
		Accepted bool `json:"formal_golden_acceptance"`
		Sources  []struct {
			ID          string `json:"id"`
			Hash        string `json:"sha256"`
			LicenseHash string `json:"license_sha256"`
			Entries     int    `json:"entries"`
		} `json:"sources"`
	}
	if e = json.Unmarshal(raw, &manifest); e != nil {
		t.Fatal(e)
	}
	if manifest.Accepted {
		t.Fatal("provenance fixture alone is not formal golden acceptance")
	}
	entries := []corpusEntry{}
	names := map[string]bool{}
	for _, source := range manifest.Sources {
		if source.ID != "ptt" && source.ID != "ptn" && source.ID != "go-ptn" && source.ID != "thcolin" {
			t.Fatal("unreviewed source")
		}
		data, e := os.ReadFile(filepath.Join(root, source.ID+".json"))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != source.Hash {
			t.Fatal("upstream annotation modified", source.ID)
		}
		license, e := os.ReadFile(filepath.Join(root, source.ID+".LICENSE"))
		if e != nil {
			t.Fatal(e)
		}
		h = sha256.Sum256(license)
		if hex.EncodeToString(h[:]) != source.LicenseHash {
			t.Fatal("license changed")
		}
		var corpus upstreamCorpus
		if e = json.Unmarshal(data, &corpus); e != nil {
			t.Fatal(e)
		}
		if corpus.License != "MIT" || len(corpus.Entries) != source.Entries {
			t.Fatal("source count/license mismatch")
		}
		for _, entry := range corpus.Entries {
			if entry.Name == "" || len(entry.Expected) == 0 {
				t.Fatal("empty annotation")
			}
			names[entry.Name] = true
		}
		entries = append(entries, corpus.Entries...)
	}
	if len(names) != manifest.Unique {
		t.Fatal("unique count drift")
	}
	return entries
}
func corpusNumbers(raw json.RawMessage) []int {
	var scalar int
	if json.Unmarshal(raw, &scalar) == nil {
		return []int{scalar}
	}
	var list []int
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	return nil
}
func TestMITCorpusAcceptedCoverageRespectsSpecifiedFields(t *testing.T) {
	entries := readMITCorpora(t)
	for _, entry := range entries {
		r := media.RecognizeRelease(entry.Name)
		if r.Reason == "" || r.ParserVersion != media.EpisodeParserVersion || r.Confidence < 0 || r.Confidence > 1 {
			t.Errorf("invalid recognition metadata %q", entry.Name)
		}
		if r.RequiresReview {
			if len(r.UnitKeys) > 0 {
				t.Errorf("review granted units %q %+v", entry.Name, r)
			}
			continue
		}
		var kind string
		_ = json.Unmarshal(entry.Expected["type"], &kind)
		if kind == "episode" && r.MediaType == media.TypeMovie {
			t.Errorf("episode treated as movie %q", entry.Name)
			continue
		}
		if r.MediaType != media.TypeTV {
			continue
		}
		_, hasSeason := entry.Expected["season"]
		_, hasEpisode := entry.Expected["episode"]
		if !hasSeason && !hasEpisode {
			continue
		} // A title-only assertion does not claim unknown season/episode.
		seasons := corpusNumbers(entry.Expected["season"])
		episodes := corpusNumbers(entry.Expected["episode"])
		if hasSeason && (len(seasons) != 1 || r.Season != seasons[0]) {
			t.Errorf("invented season %q -> %d, source %s", entry.Name, r.Season, entry.Expected["season"])
		}
		if hasEpisode {
			if len(r.Episodes) != len(episodes) {
				t.Errorf("incomplete accepted coverage %q -> %v, source %s", entry.Name, r.Episodes, entry.Expected["episode"])
			}
			allowed := map[int]bool{}
			for _, episode := range episodes {
				allowed[episode] = true
			}
			for _, episode := range r.Episodes {
				if !allowed[episode] {
					t.Errorf("invented episode %q -> %d, source %s", entry.Name, episode, entry.Expected["episode"])
				}
			}
		}
	}
}
