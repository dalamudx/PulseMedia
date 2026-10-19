package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.MetadataDetailBrowser = (*Client)(nil)

// GetMediaCredits reads display evidence only; cast never grants work identity.
func (c *Client) GetMediaCredits(ctx context.Context, mediaType string, id int64, language string) (*ports.MetadataCredits, error) {
	if (mediaType != "movie" && mediaType != "tv") || id <= 0 {
		return nil, ports.ErrRejected
	}
	if language == "" {
		language = "zh-CN"
	}
	params := url.Values{"language": {language}}
	raw, _, err := c.doGet(ctx, fmt.Sprintf("%s/%s/%d/credits?%s", c.baseURL, mediaType, id, params.Encode()), "")
	if errors.Is(err, errNotModified) {
		return nil, ports.ErrBadResponse
	}
	if err != nil {
		return nil, err
	}
	var response struct {
		ID   int64 `json:"id"`
		Cast []struct {
			ID          int64   `json:"id"`
			Name        string  `json:"name"`
			Character   string  `json:"character"`
			ProfilePath *string `json:"profile_path"`
		} `json:"cast"`
	}
	if json.Unmarshal(raw, &response) != nil || response.ID != id || response.Cast == nil {
		return nil, ports.ErrBadResponse
	}
	out := &ports.MetadataCredits{TMDBID: id, MediaType: mediaType, Cast: make([]ports.MetadataCastMember, 0, min(len(response.Cast), 24))}
	for _, actor := range response.Cast {
		if actor.ID <= 0 || strings.TrimSpace(actor.Name) == "" {
			return nil, ports.ErrBadResponse
		}
		// Validate the full provider array even when the display cap is reached.
		if len(out.Cast) < 24 {
			out.Cast = append(out.Cast, ports.MetadataCastMember{
				TMDBID: actor.ID, Name: actor.Name, Character: actor.Character,
				ProfileURL: detailImageURL(actor.ProfilePath),
			})
		}
	}
	return out, nil
}

// GetTVSeasonDetails preserves the provider's actual episode numbers and order.
func (c *Client) GetTVSeasonDetails(ctx context.Context, id int64, season int, language string) (*ports.MetadataSeasonDetails, error) {
	if id <= 0 || season < 0 {
		return nil, ports.ErrRejected
	}
	if language == "" {
		language = "zh-CN"
	}
	params := url.Values{"language": {language}}
	raw, _, err := c.doGet(ctx, fmt.Sprintf("%s/tv/%d/season/%d?%s", c.baseURL, id, season, params.Encode()), "")
	if errors.Is(err, errNotModified) {
		return nil, ports.ErrBadResponse
	}
	if err != nil {
		return nil, err
	}
	var response struct {
		SeasonNumber *int `json:"season_number"`
		Episodes     []struct {
			ID           int64           `json:"id"`
			Number       int             `json:"episode_number"`
			SeasonNumber json.RawMessage `json:"season_number"`
			ShowID       json.RawMessage `json:"show_id"`
			Name         string          `json:"name"`
			Overview     string          `json:"overview"`
			AirDate      string          `json:"air_date"`
			StillPath    *string         `json:"still_path"`
		} `json:"episodes"`
	}
	if json.Unmarshal(raw, &response) != nil || response.SeasonNumber == nil || *response.SeasonNumber != season || response.Episodes == nil || len(response.Episodes) > 5000 {
		return nil, ports.ErrBadResponse
	}
	out := &ports.MetadataSeasonDetails{TMDBID: id, SeasonNumber: season, Episodes: make([]ports.MetadataEpisodeDetails, 0, len(response.Episodes))}
	seenIDs := make(map[int64]bool, len(response.Episodes))
	seenNumbers := make(map[int]bool, len(response.Episodes))
	for _, episode := range response.Episodes {
		if episode.ID <= 0 || episode.Number <= 0 || seenIDs[episode.ID] || seenNumbers[episode.Number] ||
			!detailNumberMatches(episode.SeasonNumber, int64(season)) || !detailNumberMatches(episode.ShowID, id) {
			return nil, ports.ErrBadResponse
		}
		if episode.AirDate != "" {
			date, err := time.Parse("2006-01-02", episode.AirDate)
			if err != nil || len(episode.AirDate) != len("2006-01-02") || date.Year() < 1 {
				return nil, ports.ErrBadResponse
			}
		}
		seenIDs[episode.ID] = true
		seenNumbers[episode.Number] = true
		out.Episodes = append(out.Episodes, ports.MetadataEpisodeDetails{
			TMDBID: episode.ID, EpisodeNumber: episode.Number, Name: episode.Name,
			Overview: episode.Overview, AirDate: episode.AirDate, StillURL: detailImageURL(episode.StillPath),
		})
	}
	out.Complete = true
	return out, nil
}

func detailNumberMatches(raw json.RawMessage, expected int64) bool {
	if len(raw) == 0 {
		return true
	}
	var number *int64
	return json.Unmarshal(raw, &number) == nil && number != nil && *number == expected
}

func detailImageURL(path *string) string {
	if path == nil || len(*path) < 2 || (*path)[0] != '/' || strings.Contains(*path, "..") {
		return ""
	}
	// TMDB image paths are single file tokens, never URLs or navigable paths.
	for _, ch := range (*path)[1:] {
		validTokenChar := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.'
		if !validTokenChar {
			return ""
		}
	}
	if *path == "/." {
		return ""
	}
	return catalogImageURL(path)
}
