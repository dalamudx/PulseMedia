package tmdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.TVDBEpisodeResolver = (*Client)(nil)

func (c *Client) ResolveTVDBEpisode(ctx context.Context, tmdbSeriesID, tvdbEpisodeID int64) (*ports.TVDBEpisodeResolution, error) {
	if tmdbSeriesID <= 0 || tvdbEpisodeID <= 0 {
		return nil, ports.ErrRejected
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	lookup, err := c.tvdbEvidenceRead(bounded, fmt.Sprintf("%s/find/%d?external_source=tvdb_id", c.baseURL, tvdbEpisodeID))
	if err != nil {
		return nil, err
	}
	var rows []json.RawMessage
	if json.Unmarshal(lookup["tv_episode_results"], &rows) != nil || rows == nil {
		return nil, ports.ErrBadResponse
	}
	if len(rows) == 0 {
		return nil, ports.ErrNotFound
	}
	if len(rows) != 1 {
		return nil, ports.ErrBadResponse
	}
	found, err := tvdbEvidenceObject(rows[0])
	if err != nil {
		return nil, err
	}
	id, validID := tvdbEvidenceInteger(found["id"])
	series, validSeries := tvdbEvidenceInteger(found["show_id"])
	season, validSeason := tvdbEvidenceInteger(found["season_number"])
	episode, validEpisode := tvdbEvidenceInteger(found["episode_number"])
	if !validID || id <= 0 || !validSeries || series != tmdbSeriesID || !validSeason || season < 0 || season > 999 || !validEpisode || episode <= 0 || episode > 9999 {
		return nil, ports.ErrBadResponse
	}
	// Read the actual TMDB episode before publishing metadata: Find is identity evidence,
	// not permission to relabel a TVDB date or ordinal as a canonical TMDB fact.
	native, err := c.tvdbEvidenceRead(bounded, fmt.Sprintf("%s/tv/%d/season/%d/episode/%d", c.baseURL, series, season, episode))
	if err != nil {
		return nil, err
	}
	for key, want := range map[string]int64{"id": id, "season_number": season, "episode_number": episode} {
		got, ok := tvdbEvidenceInteger(native[key])
		if !ok || got != want {
			return nil, ports.ErrBadResponse
		}
	}
	if raw, exists := native["show_id"]; exists {
		got, ok := tvdbEvidenceInteger(raw)
		if !ok || got != series {
			return nil, ports.ErrBadResponse
		}
	}
	rawDate, exists := native["air_date"]
	if !exists {
		return nil, ports.ErrBadResponse
	}
	var date *string
	if json.Unmarshal(rawDate, &date) != nil {
		return nil, ports.ErrBadResponse
	}
	result := &ports.TVDBEpisodeResolution{TMDBSeriesID: series, TVDBEpisodeID: tvdbEpisodeID, SeasonNumber: int(season), Episode: ports.TVEpisodeMetadata{TMDBEpisodeID: id, EpisodeNumber: int(episode)}}
	if date != nil && *date != "" {
		parsed, e := time.Parse(time.DateOnly, *date)
		if e != nil || parsed.Year() < 1 || parsed.Format(time.DateOnly) != *date {
			return nil, ports.ErrBadResponse
		}
		result.Episode.AirDate = &parsed
	}
	return result, nil
}
func (c *Client) tvdbEvidenceRead(ctx context.Context, path string) (map[string]json.RawMessage, error) {
	raw, _, err := c.doGet(ctx, path, "")
	if errors.Is(err, errNotModified) {
		return nil, ports.ErrBadResponse
	}
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return tvdbEvidenceObject(raw)
}
func tvdbEvidenceInteger(raw []byte) (int64, bool) {
	var value *int64
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return 0, false
	}
	return *value, true
}
func tvdbEvidenceObject(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, ports.ErrBadResponse
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, ports.ErrBadResponse
	}
	result := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for d.More() {
		token, e := d.Token()
		if e != nil {
			return nil, ports.ErrBadResponse
		}
		key, ok := token.(string)
		folded := strings.ToLower(key)
		if !ok || seen[folded] {
			return nil, ports.ErrBadResponse
		}
		seen[folded] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ports.ErrBadResponse
		}
		result[key] = value
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') {
		return nil, ports.ErrBadResponse
	}
	if _, e := d.Token(); e != io.EOF {
		return nil, ports.ErrBadResponse
	}
	return result, nil
}
