package search

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const TVEpisodeQuerySchemaVersion = "tv-episode-query-v1"

// CanonicalTVEpisodeQuery freezes the complete non-secret request, including its
// catalog identity and one of the four scheduled page windows. It grants no authority.
func CanonicalTVEpisodeQuery(input ports.TVEpisodeQuery) (ports.TVEpisodeQuery, []byte, string, error) {
	if input.SchemaVersion == "" {
		input.SchemaVersion = TVEpisodeQuerySchemaVersion
	}
	if input.Mode == "" {
		input.Mode = "tvsearch"
	}
	if input.SeriesNamespace == "" {
		input.SeriesNamespace = "tmdb.tv"
	}
	if input.SchemaVersion != TVEpisodeQuerySchemaVersion || input.Mode != "tvsearch" || input.SeriesNamespace != "tmdb.tv" || input.SeriesID <= 0 ||
		!utf8.ValidString(input.Keyword) || strings.ContainsFunc(input.Keyword, unicode.IsControl) {
		return ports.TVEpisodeQuery{}, nil, "", ports.ErrBadResponse
	}
	input.Keyword = strings.TrimSpace(input.Keyword)
	if input.Keyword == "" || len(input.Keyword) > 512 || input.Season < 1 || input.Season > 999 || input.Episode < 1 || input.Episode > 9999 ||
		input.Categories == nil || len(input.Categories) > 100 || input.Offset < 0 || input.Offset > 150 || input.Offset%50 != 0 || input.Limit != 50 {
		return ports.TVEpisodeQuery{}, nil, "", ports.ErrBadResponse
	}
	input.Categories = append(make([]int32, 0, len(input.Categories)), input.Categories...)
	slices.Sort(input.Categories)
	for i, category := range input.Categories {
		if category <= 0 || (i > 0 && category == input.Categories[i-1]) {
			return ports.TVEpisodeQuery{}, nil, "", ports.ErrBadResponse
		}
	}
	canonical, err := json.Marshal(input)
	if err != nil {
		return ports.TVEpisodeQuery{}, nil, "", ports.ErrBadResponse
	}
	digest := sha256.Sum256(canonical)
	return input, canonical, hex.EncodeToString(digest[:]), nil
}
