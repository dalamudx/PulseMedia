package classification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"slices"
	"strings"
	"unicode"
)

// MetadataFacts validates provider facts and canonicalizes unordered sets for preview binding.
func MetadataFacts(kind string, genres []string, language string, countries []string) (Facts, error) {
	f := Facts{MediaType: kind, Genres: slices.Clone(genres), Countries: slices.Clone(countries)}
	if language != "" {
		f.Languages = []string{language}
	}
	for _, set := range [][]string{f.Genres, f.Languages, f.Countries} {
		if len(set) > 64 {
			return Facts{}, ErrInvalid
		}
		for _, v := range set {
			if v == "" || len(v) > 64 || strings.TrimSpace(v) != v || strings.ContainsFunc(v, unicode.IsControl) {
				return Facts{}, ErrInvalid
			}
		}
	}
	if Validate([]Rule{{ID: "metadata", MediaType: kind, RootID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Languages: f.Languages, Countries: f.Countries}}) != nil {
		return Facts{}, ErrInvalid
	}
	slices.Sort(f.Genres)
	f.Genres = slices.Compact(f.Genres)
	slices.Sort(f.Countries)
	f.Countries = slices.Compact(f.Countries)
	return f, nil
}
func SelectionStamp(version int64, identity string, f Facts, root uuid.UUID, generation int64) string {
	f.Genres = slices.Clone(f.Genres)
	f.Countries = slices.Clone(f.Countries)
	f.Languages = slices.Clone(f.Languages)
	slices.Sort(f.Genres)
	slices.Sort(f.Countries)
	slices.Sort(f.Languages)
	raw, _ := json.Marshal([]any{"classification.preview.v1", version, identity, f, root, generation})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
