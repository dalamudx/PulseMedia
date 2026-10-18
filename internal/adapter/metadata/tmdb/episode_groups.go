package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"regexp"
)

var _ ports.TVEpisodeGroupProvider = (*Client)(nil)
var episodeGroupID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (c *Client) ListTVEpisodeGroups(ctx context.Context, id int64) ([]ports.TVEpisodeGroupSummary, error) {
	if id < 1 {
		return nil, ports.ErrBadResponse
	}
	raw, _, e := c.doGet(ctx, fmt.Sprintf("%s/tv/%d/episode_groups", c.baseURL, id), "")
	if e != nil {
		return nil, e
	}
	var data struct {
		Results *[]struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Type     *int   `json:"type"`
			Episodes *int   `json:"episode_count"`
			Groups   *int   `json:"group_count"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Results == nil || len(*data.Results) > 500 {
		return nil, ports.ErrBadResponse
	}
	out := []ports.TVEpisodeGroupSummary{}
	seen := map[string]bool{}
	for _, g := range *data.Results {
		if !episodeGroupID.MatchString(g.ID) || seen[g.ID] || len([]rune(g.Name)) > 400 || g.Type == nil || *g.Type < 1 || *g.Type > 7 || g.Episodes == nil || *g.Episodes < 1 || *g.Episodes > 5000 || g.Groups == nil || *g.Groups < 1 || *g.Groups > 500 {
			return nil, ports.ErrBadResponse
		}
		seen[g.ID] = true
		out = append(out, ports.TVEpisodeGroupSummary{ID: g.ID, Name: g.Name, Type: *g.Type, EpisodeCount: *g.Episodes, GroupCount: *g.Groups})
	}
	return out, nil
}
func (c *Client) GetTVEpisodeGroup(ctx context.Context, series int64, id string) (*ports.TVEpisodeGroupSnapshot, error) {
	if series < 1 || !episodeGroupID.MatchString(id) {
		return nil, ports.ErrBadResponse
	}
	groups, e := c.ListTVEpisodeGroups(ctx, series)
	if e != nil {
		return nil, e
	}
	var summary *ports.TVEpisodeGroupSummary
	for i := range groups {
		if groups[i].ID == id {
			copy := groups[i]
			summary = &copy
			break
		}
	}
	if summary == nil {
		return nil, ports.ErrNotFound
	}
	raw, _, e := c.doGet(ctx, fmt.Sprintf("%s/tv/episode_group/%s", c.baseURL, id), "")
	if e != nil {
		return nil, e
	}
	var data struct {
		ID         string `json:"id"`
		Type       *int   `json:"type"`
		Episodes   *int   `json:"episode_count"`
		GroupCount *int   `json:"group_count"`
		Groups     *[]struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Order    *int   `json:"order"`
			Episodes *[]struct {
				ID     int64 `json:"id"`
				Season *int  `json:"season_number"`
				Number *int  `json:"episode_number"`
				Order  *int  `json:"order"`
			} `json:"episodes"`
		} `json:"groups"`
	}
	if json.Unmarshal(raw, &data) != nil || data.ID != id || data.Type == nil || *data.Type != summary.Type || data.Episodes == nil || *data.Episodes != summary.EpisodeCount || data.GroupCount == nil || *data.GroupCount != summary.GroupCount || data.Groups == nil || len(*data.Groups) != summary.GroupCount {
		return nil, ports.ErrBadResponse
	}
	out := &ports.TVEpisodeGroupSnapshot{SeriesID: series, Summary: *summary, Parts: []ports.TVEpisodeGroupPart{}}
	seenPart := map[string]bool{}
	seenOrder := map[int]bool{}
	seenEpisode := map[int64]bool{}
	canonical := map[[2]int]bool{}
	total := 0
	for _, g := range *data.Groups {
		if !episodeGroupID.MatchString(g.ID) || seenPart[g.ID] || len([]rune(g.Name)) > 400 || g.Order == nil || *g.Order < 0 || *g.Order > 9999 || seenOrder[*g.Order] || g.Episodes == nil || len(*g.Episodes) < 1 || len(*g.Episodes) > 5000 {
			return nil, ports.ErrBadResponse
		}
		seenPart[g.ID] = true
		seenOrder[*g.Order] = true
		part := ports.TVEpisodeGroupPart{ID: g.ID, Name: g.Name, Order: *g.Order, Episodes: []ports.TVEpisodeGroupEpisode{}}
		orders := map[int]bool{}
		for _, ep := range *g.Episodes {
			if ep.ID < 1 || seenEpisode[ep.ID] || ep.Season == nil || *ep.Season < 0 || *ep.Season > 999 || ep.Number == nil || *ep.Number < 1 || *ep.Number > 9999 || ep.Order == nil || *ep.Order < 0 || *ep.Order > 9999 || orders[*ep.Order] || canonical[[2]int{*ep.Season, *ep.Number}] {
				return nil, ports.ErrBadResponse
			}
			seenEpisode[ep.ID] = true
			canonical[[2]int{*ep.Season, *ep.Number}] = true
			orders[*ep.Order] = true
			part.Episodes = append(part.Episodes, ports.TVEpisodeGroupEpisode{ID: ep.ID, Season: *ep.Season, Episode: *ep.Number, Order: *ep.Order})
			total++
			if total > 5000 {
				return nil, ports.ErrBadResponse
			}
		}
		out.Parts = append(out.Parts, part)
	}
	if total != summary.EpisodeCount {
		return nil, ports.ErrBadResponse
	}
	return out, nil
}
