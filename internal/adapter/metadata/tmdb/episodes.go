package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/url"
	"time"
)

var _ ports.TVEpisodeProvider = (*Client)(nil)

func (c *Client) GetTVSeasonMetadata(ctx context.Context, id int64, season int, language string) (*ports.TVSeasonMetadata, error) {
	if id <= 0 || season < 0 || season > 999 {
		return nil, ports.ErrBadResponse
	}
	if language == "" {
		language = "zh-CN"
	}
	raw, _, e := c.doGet(ctx, fmt.Sprintf("%s/tv/%d/season/%d?language=%s", c.baseURL, id, season, url.QueryEscape(language)), "")
	if e != nil {
		return nil, e
	}
	var response struct {
		SeasonNumber int `json:"season_number"`
		Episodes     *[]struct {
			ID     int64   `json:"id"`
			Number int     `json:"episode_number"`
			Season int     `json:"season_number"`
			Date   *string `json:"air_date"`
		} `json:"episodes"`
	}
	if json.Unmarshal(raw, &response) != nil || response.SeasonNumber != season || response.Episodes == nil || len(*response.Episodes) > 5000 {
		return nil, ports.ErrBadResponse
	}
	out := &ports.TVSeasonMetadata{SeasonNumber: season, Complete: true, Episodes: []ports.TVEpisodeMetadata{}}
	seen := map[int]bool{}
	seenIDs := map[int64]bool{}
	for _, episode := range *response.Episodes {
		if episode.ID < 1 || seenIDs[episode.ID] || episode.Number < 1 || episode.Season != season || seen[episode.Number] {
			return nil, ports.ErrBadResponse
		}
		seen[episode.Number] = true
		seenIDs[episode.ID] = true
		var date *time.Time
		if episode.Date != nil && *episode.Date != "" {
			parsed, e := time.Parse("2006-01-02", *episode.Date)
			if e != nil {
				return nil, ports.ErrBadResponse
			}
			date = &parsed
		}
		out.Episodes = append(out.Episodes, ports.TVEpisodeMetadata{TMDBEpisodeID: episode.ID, EpisodeNumber: episode.Number, AirDate: date})
	}
	return out, nil
}
