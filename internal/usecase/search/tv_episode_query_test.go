package search

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func tvQueryFixture() ports.TVEpisodeQuery {
	return ports.TVEpisodeQuery{SeriesID: 42, Keyword: "Example 剧", Season: 2, Episode: 3, Categories: []int32{5040, 5000}, Limit: 50}
}

func TestCanonicalTVEpisodeQueryIdentity(t *testing.T) {
	input := tvQueryFixture()
	input.Keyword = "  " + input.Keyword + "  "
	got, raw, hash, err := CanonicalTVEpisodeQuery(input)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema_version":"tv-episode-query-v1","mode":"tvsearch","series_namespace":"tmdb.tv","series_id":42,"keyword":"Example 剧","season":2,"episode":3,"categories":[5000,5040],"offset":0,"limit":50}`
	if string(raw) != want || got.SchemaVersion != TVEpisodeQuerySchemaVersion {
		t.Fatalf("unexpected canonical request: %s", raw)
	}
	sum := sha256.Sum256([]byte(want))
	if hash != hex.EncodeToString(sum[:]) || len(hash) != 64 || hash != strings.ToLower(hash) {
		t.Fatal("incorrect digest")
	}
	var decoded ports.TVEpisodeQuery
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	_, again, againHash, err := CanonicalTVEpisodeQuery(decoded)
	if err != nil || !bytes.Equal(raw, again) || hash != againHash {
		t.Fatal("round trip changed identity", err)
	}
	permuted := tvQueryFixture()
	permuted.Categories = []int32{5000, 5040}
	_, permutedRaw, permutedHash, err := CanonicalTVEpisodeQuery(permuted)
	if err != nil || !bytes.Equal(raw, permutedRaw) || hash != permutedHash {
		t.Fatal("category order changed identity", err)
	}
	for _, forbidden := range []string{"apikey", "credential", "passkey", "url", "TitleYear"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatal("non-query metadata in proof")
		}
	}
	got.Categories[0] = 1
	input.Categories[0] = 2
	if decoded.Categories[0] != 5000 || string(raw) != want || input.Categories[1] != 5000 || got.Categories[1] != 5040 {
		t.Fatal("query copies share storage")
	}
	for name, mutate := range map[string]func(*ports.TVEpisodeQuery){
		"series":   func(q *ports.TVEpisodeQuery) { q.SeriesID++ },
		"keyword":  func(q *ports.TVEpisodeQuery) { q.Keyword += " other" },
		"season":   func(q *ports.TVEpisodeQuery) { q.Season++ },
		"episode":  func(q *ports.TVEpisodeQuery) { q.Episode++ },
		"category": func(q *ports.TVEpisodeQuery) { q.Categories = []int32{5000} },
		"page":     func(q *ports.TVEpisodeQuery) { q.Offset = 50 },
	} {
		t.Run(name, func(t *testing.T) {
			q := tvQueryFixture()
			mutate(&q)
			_, _, changed, err := CanonicalTVEpisodeQuery(q)
			if err != nil || changed == hash {
				t.Fatal("query change not bound", err)
			}
		})
	}
}

func TestCanonicalTVEpisodeQueryRejectsAmbiguousMaterial(t *testing.T) {
	cases := map[string]func(*ports.TVEpisodeQuery){
		"schema":               func(q *ports.TVEpisodeQuery) { q.SchemaVersion = "v2" },
		"schema whitespace":    func(q *ports.TVEpisodeQuery) { q.SchemaVersion = " " },
		"mode":                 func(q *ports.TVEpisodeQuery) { q.Mode = "movie" },
		"namespace":            func(q *ports.TVEpisodeQuery) { q.SeriesNamespace = "tvdb.tv" },
		"series zero":          func(q *ports.TVEpisodeQuery) { q.SeriesID = 0 },
		"series negative":      func(q *ports.TVEpisodeQuery) { q.SeriesID = -1 },
		"blank":                func(q *ports.TVEpisodeQuery) { q.Keyword = "   " },
		"utf8":                 func(q *ports.TVEpisodeQuery) { q.Keyword = string([]byte{0xff}) },
		"too long":             func(q *ports.TVEpisodeQuery) { q.Keyword = strings.Repeat("剧", 171) },
		"season zero":          func(q *ports.TVEpisodeQuery) { q.Season = 0 },
		"season overflow":      func(q *ports.TVEpisodeQuery) { q.Season = 1000 },
		"episode zero":         func(q *ports.TVEpisodeQuery) { q.Episode = 0 },
		"episode overflow":     func(q *ports.TVEpisodeQuery) { q.Episode = 10000 },
		"nil categories":       func(q *ports.TVEpisodeQuery) { q.Categories = nil },
		"duplicate categories": func(q *ports.TVEpisodeQuery) { q.Categories = []int32{5000, 5040, 5000} },
		"zero category":        func(q *ports.TVEpisodeQuery) { q.Categories = []int32{0} },
		"negative category":    func(q *ports.TVEpisodeQuery) { q.Categories = []int32{-1} },
		"category count":       func(q *ports.TVEpisodeQuery) { q.Categories = make([]int32, 101) },
		"negative offset":      func(q *ports.TVEpisodeQuery) { q.Offset = -50 },
		"offset step":          func(q *ports.TVEpisodeQuery) { q.Offset = 1 },
		"offset bound":         func(q *ports.TVEpisodeQuery) { q.Offset = 200 },
		"limit zero":           func(q *ports.TVEpisodeQuery) { q.Limit = 0 },
		"limit negative":       func(q *ports.TVEpisodeQuery) { q.Limit = -1 },
		"limit other":          func(q *ports.TVEpisodeQuery) { q.Limit = 100 },
	}
	for _, control := range []string{"\x00", "\t", "\n", "\r", "\x7f", "\u0085"} {
		cases["control"+control] = func(q *ports.TVEpisodeQuery) { q.Keyword = control + "Example" }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			q := tvQueryFixture()
			mutate(&q)
			got, raw, hash, err := CanonicalTVEpisodeQuery(q)
			if !errors.Is(err, ports.ErrBadResponse) || !reflect.DeepEqual(got, ports.TVEpisodeQuery{}) || raw != nil || hash != "" {
				t.Fatal("invalid request returned material", err)
			}
		})
	}
	got, raw, hash, err := CanonicalTVEpisodeQuery(ports.TVEpisodeQuery{})
	if err == nil || !reflect.DeepEqual(got, ports.TVEpisodeQuery{}) || raw != nil || hash != "" {
		t.Fatal("zero query accepted")
	}
}

func TestCanonicalTVEpisodeQueryBounds(t *testing.T) {
	q := tvQueryFixture()
	q.Keyword = strings.Repeat("x", 512)
	q.Season, q.Episode, q.Offset = 999, 9999, 150
	q.Categories = make([]int32, 100)
	for i := range q.Categories {
		q.Categories[i] = int32(i + 1)
	}
	q.Categories[99] = 2147483647
	if _, _, _, err := CanonicalTVEpisodeQuery(q); err != nil {
		t.Fatal("valid upper bounds rejected", err)
	}
	q.Categories = []int32{}
	got, raw, _, err := CanonicalTVEpisodeQuery(q)
	if err != nil || got.Categories == nil || !bytes.Contains(raw, []byte(`"categories":[]`)) {
		t.Fatal("explicit empty categories lost", err)
	}
}
